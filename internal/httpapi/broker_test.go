package httpapi

import "testing"

func TestBrokerDisconnectsRevokedUsersAndSessions(t *testing.T) {
	broker := newBroker()
	alice, unsubscribeAlice := broker.subscribe("alice", "alice-session")
	defer unsubscribeAlice()
	bob, unsubscribeBob := broker.subscribe("bob", "bob-session")
	defer unsubscribeBob()

	broker.disconnectUser("alice")
	if _, open := <-alice; open {
		t.Fatal("alice live connection remained open after user revocation")
	}
	select {
	case _, open := <-bob:
		if !open {
			t.Fatal("bob live connection was closed by alice revocation")
		}
	default:
	}

	broker.disconnectSession("bob-session")
	if _, open := <-bob; open {
		t.Fatal("bob live connection remained open after session revocation")
	}
}
