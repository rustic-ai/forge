package guild

import (
	"fmt"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

// RouteBuilder provides a fluent API for constructing a RoutingRule.
type RouteBuilder struct {
	rule protocol.RoutingRule
	err  error
}

// NewRouteBuilder creates a RouteBuilder from a source identifier.
// The source can be:
//   - protocol.AgentTag: sets the rule's Agent field
//   - protocol.AgentSpec: extracts ID+Name into an AgentTag
//   - string: treated as an agent type (class name), sets AgentType
func NewRouteBuilder(from interface{}) *RouteBuilder {
	rb := &RouteBuilder{}
	switch v := from.(type) {
	case protocol.AgentTag:
		rb.rule.Agent = &v
	case *protocol.AgentTag:
		rb.rule.Agent = v
	case protocol.AgentSpec:
		rb.rule.Agent = &protocol.AgentTag{
			ID:   strPtr(v.ID),
			Name: strPtr(v.Name),
		}
	case *protocol.AgentSpec:
		rb.rule.Agent = &protocol.AgentTag{
			ID:   strPtr(v.ID),
			Name: strPtr(v.Name),
		}
	case string:
		rb.rule.AgentType = strPtr(v)
	default:
		rb.err = fmt.Errorf("NewRouteBuilder: unsupported source type %T", from)
	}
	return rb
}

// FromMethod sets the method name filter on the routing rule.
func (rb *RouteBuilder) FromMethod(methodName string) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.MethodName = strPtr(methodName)
	return rb
}

// OnMessageFormat sets the message format filter on the routing rule.
func (rb *RouteBuilder) OnMessageFormat(format string) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.MessageFormat = strPtr(format)
	return rb
}

// FilterOnOrigin sets the origin filter on the routing rule.
// Any nil parameter is left unset in the filter.
func (rb *RouteBuilder) FilterOnOrigin(sender *protocol.AgentTag, topic *string, messageFormat *string) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.OriginFilter = &protocol.RoutingOrigin{
		OriginSender:        sender,
		OriginTopic:         topic,
		OriginMessageFormat: messageFormat,
	}
	return rb
}

// SetDestinationTopics sets the destination topics.
func (rb *RouteBuilder) SetDestinationTopics(topics protocol.Topics) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	if rb.rule.Destination == nil {
		rb.rule.Destination = &protocol.RoutingDestination{}
	}
	rb.rule.Destination.Topics = topics
	return rb
}

// AddRecipients appends recipient agent tags to the destination.
func (rb *RouteBuilder) AddRecipients(recipients []protocol.AgentTag) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	if rb.rule.Destination == nil {
		rb.rule.Destination = &protocol.RoutingDestination{}
	}
	rb.rule.Destination.RecipientList = append(rb.rule.Destination.RecipientList, recipients...)
	return rb
}

// MarkForwarded sets whether the message should be marked as forwarded.
func (rb *RouteBuilder) MarkForwarded(forwarded bool) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.MarkForwarded = forwarded
	return rb
}

// SetRouteTimes sets how many times this route should fire.
func (rb *RouteBuilder) SetRouteTimes(times int) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.RouteTimes = &times
	return rb
}

// SetProcessStatus sets the process status on the routing rule.
func (rb *RouteBuilder) SetProcessStatus(status protocol.ProcessStatus) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.ProcessStatus = &status
	return rb
}

// SetReason sets the reason string on the routing rule.
func (rb *RouteBuilder) SetReason(reason string) *RouteBuilder {
	if rb.err != nil {
		return rb
	}
	rb.rule.Reason = strPtr(reason)
	return rb
}

// Build returns the constructed RoutingRule.
func (rb *RouteBuilder) Build() (protocol.RoutingRule, error) {
	if rb.err != nil {
		return protocol.RoutingRule{}, rb.err
	}
	return rb.rule, nil
}
