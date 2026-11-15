package localtel

import "testing"

func TestHubRoutesCallOnlyBetweenParticipants(t *testing.T) {
	hub := NewHub()
	caller := hub.Register("caller", "device-a")
	callee := hub.Register("callee", "device-b")
	stranger := hub.Register("stranger", "device-c")
	defer hub.Unregister(caller)
	defer hub.Unregister(callee)
	defer hub.Unregister(stranger)

	if !hub.BeginCall("call-1", "caller", "callee") {
		t.Fatal("failed to establish call route")
	}
	invite := SignalMessage{Type: "invite", CallID: "call-1", Number: "12345"}
	if got := hub.DispatchCall("caller", caller.id, invite); got != 1 {
		t.Fatalf("invite delivered to %d clients, want 1", got)
	}
	select {
	case <-callee.SendChannel():
	default:
		t.Fatal("callee did not receive invite")
	}
	select {
	case <-stranger.SendChannel():
		t.Fatal("non-participant received invite")
	default:
	}

	if got := hub.DispatchCall("stranger", stranger.id, SignalMessage{Type: "accept", CallID: "call-1"}); got != 0 {
		t.Fatalf("non-participant routed a signal to %d clients", got)
	}
	if got := hub.DispatchCall("callee", callee.id, SignalMessage{Type: "accept", CallID: "call-1"}); got != 1 {
		t.Fatalf("accept delivered to %d clients, want 1", got)
	}
	select {
	case <-caller.SendChannel():
	default:
		t.Fatal("caller did not receive accept")
	}
}
