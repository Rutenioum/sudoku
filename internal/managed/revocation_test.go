package managed

import "testing"

func TestInstallRevokesAndRejectsDelayedRegistration(t *testing.T) {
	rig := newRig(t)
	old := rig.installIdentity(t, 1)
	conn := newFakeConn("")
	session := rig.manager.OpenSession(old, "203.0.113.9", conn)
	if session == nil {
		t.Fatal("initial registration failed")
	}
	revision, deadline := rig.manager.table.state()
	if err := rig.manager.Install(rig.manager.table.realmID, revision+1, deadline, nil); err != nil {
		t.Fatal(err)
	}
	if !conn.closed || rig.manager.LiveSessions() != 1 {
		t.Fatal("revoked session remained open")
	}
	_ = session.Close() // The relay handler has now finished counting.
	if rig.manager.LiveSessions() != 0 {
		t.Fatal("finished session was not released")
	}
	if got := rig.manager.OpenSession(old, "203.0.113.9", newFakeConn("")); got != nil {
		t.Fatal("stale admission registered after revocation")
	}
}
