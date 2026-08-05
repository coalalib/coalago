package coalago

import (
	"testing"
	"time"
)

// TestServerClose_StopsListenLoop covers the new public Close(): a server
// started with Listen() in a background goroutine must have its listenLoop
// unblock and return (no panic, no leaked goroutine) once Close() is called,
// and a repeated Close() must be a no-op that returns nil.
func TestServerClose_StopsListenLoop(t *testing.T) {
	s := NewServer()

	done := make(chan error, 1)
	go func() {
		done <- s.Listen("127.0.0.1:0")
	}()

	// Ждём, пока Listen() поднимет сокет и присвоит s.sr. Читаем поле под тем
	// же srMu, которым защищена запись в Listen() — иначе само ожидание было
	// бы гонкой относительно записи (см. go test -race).
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.srMu.Lock()
		ready := s.sr != nil
		s.srMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening in time")
		}
		time.Sleep(time.Millisecond)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Listen() returned error after Close(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listenLoop goroutine did not exit after Close()")
	}

	// Идемпотентность: повторный вызов не паникует и возвращает nil.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() call should return nil, got: %v", err)
	}
}
