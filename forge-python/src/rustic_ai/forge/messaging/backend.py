from __future__ import annotations

import logging
import os
from queue import Empty, Queue
import signal
import threading
import time
from typing import Any, Callable, Dict, List, Optional, Set
import uuid

import zmq

from rustic_ai.core.messaging.core.message import Message
from rustic_ai.core.messaging.core.messaging_backend import MessagingBackend

logger = logging.getLogger(__name__)


def _normalize_message(payload: Any) -> Message:
    if isinstance(payload, Message):
        return payload
    if isinstance(payload, (bytes, bytearray, str)):
        return Message.from_json(payload)
    return Message.model_validate(payload)


class SupervisorZmqMessagingBackend(MessagingBackend):
    """
    MessagingBackend that proxies agent dataplane calls through a local Forge
    supervisor bridge over one bidirectional ZeroMQ socket.
    """

    def __init__(
        self,
        endpoint: str,
        request_timeout_ms: int = 30_000,
        recv_poll_timeout_ms: int = 100,
        heartbeat_interval_ms: int = 10_000,
        linger_ms: int = 0,
        retry_enabled: bool = True,
        retry_initial_delay: float = 1.0,
        retry_max_delay: float = 60.0,
        max_retry_attempts: int = 5,
        max_retry_time: float = 300.0,
        crash_on_failure: bool = True,
    ) -> None:
        if not endpoint:
            raise ValueError("endpoint is required for SupervisorZmqMessagingBackend")

        self.endpoint = endpoint
        self.request_timeout_ms = request_timeout_ms
        self.recv_poll_timeout_ms = recv_poll_timeout_ms
        self.heartbeat_interval_ms = heartbeat_interval_ms
        self.linger_ms = linger_ms
        self.retry_enabled = retry_enabled
        self.retry_initial_delay = retry_initial_delay
        self.retry_max_delay = retry_max_delay
        self.max_retry_attempts = max_retry_attempts
        self.max_retry_time = max_retry_time
        self.crash_on_failure = crash_on_failure

        self._context = zmq.Context.instance()
        self._pending: Dict[str, Queue[dict[str, Any]]] = {}
        self._pending_lock = threading.Lock()
        self._handlers: Dict[str, Callable[[Message], None]] = {}
        self._handlers_lock = threading.RLock()
        self._outbound: Queue[dict[str, Any]] = Queue()
        self._deliveries: Queue[tuple[str, Message]] = Queue()
        self._shutdown_event = threading.Event()
        self._reconnect_event = threading.Event()
        self._transport_error: Optional[Exception] = None
        self._transport_error_lock = threading.Lock()
        self._io_thread: Optional[threading.Thread] = None
        self._delivery_thread: Optional[threading.Thread] = None
        self._heartbeat_thread: Optional[threading.Thread] = None

        self._start_io_thread()
        self._start_delivery_thread()
        self._start_heartbeat()

    def store_message(self, namespace: str, topic: str, message: Message) -> None:
        self._send_request(
            "publish",
            {
                "namespace": namespace,
                "topic": topic,
                "message": message.to_json(),
            },
        )

    def get_messages_for_topic(self, topic: str) -> List[Message]:
        response = self._send_request("get_messages", {"topic": topic})
        return [_normalize_message(msg) for msg in response.get("messages", [])]

    def get_messages_for_topic_since(self, topic: str, msg_id_since: int) -> List[Message]:
        response = self._send_request(
            "get_since",
            {"topic": topic, "since_id": msg_id_since},
        )
        return [_normalize_message(msg) for msg in response.get("messages", [])]

    def get_next_message_for_topic_since(self, topic: str, last_message_id: int) -> Optional[Message]:
        response = self._send_request(
            "get_next",
            {"topic": topic, "since_id": last_message_id},
        )
        message = response.get("message")
        if message is None:
            return None
        return _normalize_message(message)

    def load_subscribers(self, namespace: str) -> Dict[str, Set[str]]:
        return {}

    def subscribe(self, topic: str, handler: Callable[[Message], None]) -> None:
        with self._handlers_lock:
            self._handlers[topic] = handler

        try:
            self._send_request("subscribe", {"topic": topic})
        except Exception:
            with self._handlers_lock:
                if self._handlers.get(topic) is handler:
                    self._handlers.pop(topic, None)
            raise

    def unsubscribe(self, topic: str) -> None:
        self._send_request("unsubscribe", {"topic": topic})
        with self._handlers_lock:
            self._handlers.pop(topic, None)

    def cleanup(self) -> None:
        if self._shutdown_event.is_set():
            return

        try:
            self._send_request(
                "cleanup",
                {},
                allow_reconnect=False,
                timeout_ms=min(self.request_timeout_ms, 2_000),
            )
        except Exception:
            logger.debug("Supervisor cleanup request failed during shutdown", exc_info=True)

        self._shutdown_event.set()
        self._reconnect_event.set()
        self._fail_pending("Supervisor ZeroMQ backend is shutting down")

        if self._io_thread and self._io_thread.is_alive():
            self._io_thread.join(timeout=2)
        if self._delivery_thread and self._delivery_thread.is_alive():
            self._delivery_thread.join(timeout=2)
        if self._heartbeat_thread and self._heartbeat_thread.is_alive():
            self._heartbeat_thread.join(timeout=2)

        with self._handlers_lock:
            self._handlers.clear()

    def supports_subscription(self) -> bool:
        return True

    def get_messages_by_id(self, namespace: str, msg_ids: List[int]) -> List[Message]:
        if not msg_ids:
            return []

        response = self._send_request(
            "get_by_id",
            {"namespace": namespace, "msg_ids": msg_ids},
        )
        return [_normalize_message(msg) for msg in response.get("messages", [])]

    def _start_io_thread(self) -> None:
        self._io_thread = threading.Thread(
            target=self._io_loop,
            daemon=True,
            name="forge-supervisor-zmq-io",
        )
        self._io_thread.start()

    def _start_heartbeat(self) -> None:
        if self.heartbeat_interval_ms <= 0:
            return

        self._heartbeat_thread = threading.Thread(
            target=self._heartbeat_loop,
            daemon=True,
            name="forge-supervisor-zmq-heartbeat",
        )
        self._heartbeat_thread.start()

    def _start_delivery_thread(self) -> None:
        self._delivery_thread = threading.Thread(
            target=self._delivery_loop,
            daemon=True,
            name="forge-supervisor-zmq-delivery",
        )
        self._delivery_thread.start()

    def _heartbeat_loop(self) -> None:
        interval = self.heartbeat_interval_ms / 1000.0
        timeout_ms = max(1_000, min(self.request_timeout_ms, self.heartbeat_interval_ms))

        while not self._shutdown_event.wait(interval):
            try:
                self._send_request("ping", {}, timeout_ms=timeout_ms)
            except Exception:
                logger.debug("Supervisor ping failed", exc_info=True)

    def _io_loop(self) -> None:
        socket: Optional[zmq.Socket] = None
        poller: Optional[zmq.Poller] = None
        reconnect_cause: Exception = RuntimeError("initial supervisor ZeroMQ connect")

        try:
            while not self._shutdown_event.is_set():
                if socket is None or poller is None:
                    socket, poller = self._connect_with_retry(reconnect_cause)
                    if socket is None or poller is None:
                        return

                    try:
                        self._resubscribe_topics(socket, poller)
                    except Exception as exc:
                        reconnect_cause = exc
                        socket, poller = self._reset_socket(socket, poller)
                        continue

                try:
                    self._drain_outbound(socket)

                    if self._reconnect_event.is_set():
                        reconnect_cause = self._consume_transport_error() or RuntimeError(
                            "supervisor ZeroMQ reconnect requested"
                        )
                        socket, poller = self._reset_socket(socket, poller)
                        self._reconnect_event.clear()
                        continue

                    events = dict(poller.poll(self.recv_poll_timeout_ms))
                    if socket not in events or not (events[socket] & zmq.POLLIN):
                        continue

                    envelope = socket.recv_json()
                    self._handle_envelope(envelope)
                except zmq.Again:
                    continue
                except Exception as exc:
                    if not self.retry_enabled:
                        self._handle_critical_failure(exc)
                        return
                    reconnect_cause = exc
                    logger.warning("Supervisor ZeroMQ transport failed: %s", exc)
                    socket, poller = self._reset_socket(socket, poller)
                    self._reconnect_event.clear()
        finally:
            self._close_socket(socket, poller)

    def _connect_with_retry(self, initial_cause: Exception) -> tuple[Optional[zmq.Socket], Optional[zmq.Poller]]:
        attempt = 0
        delay = self.retry_initial_delay
        start_time = time.monotonic()
        cause = initial_cause

        while not self._shutdown_event.is_set():
            attempt += 1
            try:
                socket = self._context.socket(zmq.PAIR)
                socket.linger = self.linger_ms
                socket.connect(self.endpoint)

                poller = zmq.Poller()
                poller.register(socket, zmq.POLLIN)

                return socket, poller
            except Exception as exc:
                cause = exc

            if not self.retry_enabled:
                self._handle_critical_failure(cause)
                return None, None

            elapsed = time.monotonic() - start_time
            if attempt > self.max_retry_attempts or elapsed > self.max_retry_time:
                self._handle_critical_failure(cause)
                return None, None

            logger.warning(
                "Supervisor ZeroMQ connect attempt %s failed: %s",
                attempt,
                cause,
            )

            if self._shutdown_event.wait(delay):
                return None, None
            delay = min(delay * 2, self.retry_max_delay)

        return None, None

    def _resubscribe_topics(self, socket: zmq.Socket, poller: zmq.Poller) -> None:
        with self._handlers_lock:
            topics = list(self._handlers)

        for topic in topics:
            self._send_direct_request(socket, poller, "subscribe", {"topic": topic})

    def _drain_outbound(self, socket: zmq.Socket) -> None:
        while not self._shutdown_event.is_set():
            try:
                envelope = self._outbound.get_nowait()
            except Empty:
                return

            try:
                socket.send_json(envelope)
            except Exception as exc:
                self._mark_transport_unhealthy(exc)
                raise

    def _send_direct_request(
        self,
        socket: zmq.Socket,
        poller: zmq.Poller,
        op: str,
        payload: dict[str, Any],
    ) -> dict[str, Any]:
        request_id = str(uuid.uuid4())
        envelope = {"kind": "request", "op": op, "request_id": request_id} | payload
        socket.send_json(envelope)

        deadline = time.monotonic() + (self.request_timeout_ms / 1000.0)
        while not self._shutdown_event.is_set():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError(f"Timed out waiting for supervisor response to {op}")

            events = dict(poller.poll(max(1, int(remaining * 1000))))
            if socket not in events or not (events[socket] & zmq.POLLIN):
                continue

            incoming = socket.recv_json()
            if incoming.get("kind") == "response" and incoming.get("request_id") == request_id:
                if not incoming.get("ok", False):
                    raise RuntimeError(incoming.get("error", f"Supervisor {op} request failed"))
                return incoming

            self._handle_envelope(incoming)

        raise RuntimeError("Supervisor ZeroMQ backend is shut down")

    def _handle_envelope(self, envelope: dict[str, Any]) -> None:
        kind = envelope.get("kind")
        if kind == "response":
            request_id = envelope.get("request_id")
            if not request_id:
                return

            with self._pending_lock:
                queue = self._pending.get(request_id)

            if queue is not None:
                queue.put(envelope)
            return

        if kind == "event" and envelope.get("op") == "deliver":
            topic = envelope.get("topic")
            if not topic:
                return

            with self._handlers_lock:
                handler = self._handlers.get(topic)
            if handler is None:
                return
            self._deliveries.put((topic, _normalize_message(envelope.get("message"))))

    def _delivery_loop(self) -> None:
        while not self._shutdown_event.is_set():
            try:
                topic, message = self._deliveries.get(timeout=0.1)
            except Empty:
                continue

            with self._handlers_lock:
                handler = self._handlers.get(topic)

            if handler is None:
                continue

            try:
                handler(message)
            except Exception:
                logger.exception("Supervisor ZeroMQ delivery handler failed for topic %s", topic)

    def _send_request(
        self,
        op: str,
        payload: dict[str, Any],
        *,
        allow_reconnect: bool = True,
        timeout_ms: Optional[int] = None,
    ) -> dict[str, Any]:
        if self._shutdown_event.is_set():
            raise RuntimeError("Supervisor ZeroMQ backend is shut down")

        request_id = str(uuid.uuid4())
        queue: Queue[dict[str, Any]] = Queue(maxsize=1)

        with self._pending_lock:
            self._pending[request_id] = queue

        self._outbound.put({"kind": "request", "op": op, "request_id": request_id} | payload)

        try:
            response = queue.get(timeout=(timeout_ms or self.request_timeout_ms) / 1000.0)
        except Empty as exc:
            if allow_reconnect:
                self._mark_transport_unhealthy(exc)
            raise TimeoutError(f"Timed out waiting for supervisor response to {op}") from exc
        finally:
            with self._pending_lock:
                self._pending.pop(request_id, None)

        if not response.get("ok", False):
            raise RuntimeError(response.get("error", f"Supervisor {op} request failed"))
        return response

    def _mark_transport_unhealthy(self, cause: Exception) -> None:
        if self._shutdown_event.is_set():
            return

        if not self.retry_enabled:
            self._handle_critical_failure(cause)
            return

        with self._transport_error_lock:
            self._transport_error = cause
        self._reconnect_event.set()

    def _consume_transport_error(self) -> Optional[Exception]:
        with self._transport_error_lock:
            cause = self._transport_error
            self._transport_error = None
            return cause

    def _reset_socket(
        self,
        socket: Optional[zmq.Socket],
        poller: Optional[zmq.Poller],
    ) -> tuple[None, None]:
        self._close_socket(socket, poller)
        return None, None

    def _close_socket(self, socket: Optional[zmq.Socket], poller: Optional[zmq.Poller]) -> None:
        if socket is None:
            return

        if poller is not None:
            try:
                poller.unregister(socket)
            except KeyError:
                pass

        try:
            socket.close(linger=self.linger_ms)
        except Exception:
            logger.debug("Ignoring ZeroMQ socket close error", exc_info=True)

    def _fail_pending(self, error: str) -> None:
        with self._pending_lock:
            pending = list(self._pending.values())
            self._pending.clear()

        for queue in pending:
            try:
                queue.put_nowait({"kind": "response", "ok": False, "error": error})
            except Exception:
                continue

    def _handle_critical_failure(self, exc: Exception) -> None:
        logger.critical("Critical supervisor ZeroMQ failure detected: %s", exc)
        self._shutdown_event.set()
        self._reconnect_event.set()
        self._fail_pending(str(exc))
        if self.crash_on_failure:
            os.kill(os.getpid(), signal.SIGTERM)
