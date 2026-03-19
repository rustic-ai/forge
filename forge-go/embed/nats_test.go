package embed

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedNATS(t *testing.T) {
	en, err := StartEmbeddedNATS()
	require.NoError(t, err)
	require.NotNil(t, en)
	defer en.Close()

	require.NotEmpty(t, en.Host())
	require.NotZero(t, en.Port())
	require.NotEmpty(t, en.Addr())
	require.True(t, strings.HasPrefix(en.ClientURL(), "nats://"))

	// Pub/sub round-trip
	nc, err := en.Client()
	require.NoError(t, err)
	defer nc.Close()

	sub, err := nc.SubscribeSync("test.subject")
	require.NoError(t, err)

	err = nc.Publish("test.subject", []byte("hello"))
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), msg.Data)
}

func TestEmbeddedNATSAt_ExplicitAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	en, err := StartEmbeddedNATSAt(addr)
	require.NoError(t, err)
	require.NotNil(t, en)
	defer en.Close()

	require.Equal(t, addr, en.Addr())
}

func TestEmbeddedNATSAt_FailsWhenOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	_, err = StartEmbeddedNATSAt(ln.Addr().String())
	require.Error(t, err)
}

func TestEmbeddedNATS_JetStreamEnabled(t *testing.T) {
	en, err := StartEmbeddedNATS()
	require.NoError(t, err)
	defer en.Close()

	nc, err := en.Client()
	require.NoError(t, err)
	defer nc.Close()

	js, err := nc.JetStream()
	require.NoError(t, err)

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "TEST",
		Subjects: []string{"test.>"},
	})
	require.NoError(t, err)

	_, err = js.Publish("test.msg", []byte("jetstream-hello"))
	require.NoError(t, err)

	sub, err := js.PullSubscribe("test.msg", "test-consumer")
	require.NoError(t, err)

	msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, []byte("jetstream-hello"), msgs[0].Data)
}
