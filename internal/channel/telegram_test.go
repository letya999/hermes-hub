package channel

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// A blocked notice must reach the owner's CURRENT verified home, not the
// schedule's durable envelope target: a stale binding (chat remapped to
// another principal since enrollment) is exactly what may have caused the
// block — following it reroutes the notice to the wrong principal.
func TestRouteBlockedNoticeUsesCurrentOwnerBinding(t *testing.T) {
	ch := NewTelegram(TelegramDeps{
		UserByPrincipal: func(id string) commodel.User {
			if id != "local" {
				t.Fatalf("unexpected principal %q", id)
			}
			return commodel.User{ID: "local", TelegramIDs: []int64{111}}
		},
	})
	item := commodel.Schedule{Channel: "telegram_bot", Envelope: identity.Envelope{PrincipalID: "local", DeliveryTargetID: "telegram-480637186"}}
	d := commodel.Delivery{ChatID: 480637186, ConversationID: "telegram-480637186", DeliveryTargetID: "telegram-480637186"}
	ch.RouteBlockedNotice(item, &d)
	if d.ChatID != 111 || d.DeliveryTargetID != "telegram-111" {
		t.Fatalf("notice must follow the current owner binding: %+v", d)
	}
}

// An owner with no current telegram binding has no verified home: the notice
// must clear the address (normalize then refuses it) rather than panic or
// follow the stale envelope target.
func TestRouteBlockedNoticeNoBindingClearsAddress(t *testing.T) {
	ch := NewTelegram(TelegramDeps{
		UserByPrincipal: func(id string) commodel.User { return commodel.User{ID: id} },
	})
	item := commodel.Schedule{Channel: "telegram_bot", Envelope: identity.Envelope{PrincipalID: "local", DeliveryTargetID: "telegram-480637186"}}
	d := commodel.Delivery{ChatID: 480637186, ConversationID: "telegram-480637186", DeliveryTargetID: "telegram-480637186"}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("unbound owner must not panic: %v", r)
			}
		}()
		ch.RouteBlockedNotice(item, &d)
	}()
	if err := ch.NormalizeDelivery(&d); err == nil {
		t.Fatalf("notice to an unbound owner must not normalize: %+v", d)
	}
}
