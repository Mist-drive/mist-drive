package httpx

import "testing"

func TestProcessingState(t *testing.T) {
	srv := &Server{}

	srv.AddProcessing("u1", "docs")
	if !srv.isProcessingBlocked("u1", "docs/readme.txt") {
		t.Fatal("docs/readme.txt should be blocked under docs")
	}
	if srv.isProcessingBlocked("u1", "other/file.txt") {
		t.Fatal("other/file.txt should not be blocked")
	}

	srv.RemoveProcessing("u1", "docs")
	if srv.isProcessingBlocked("u1", "docs/readme.txt") {
		t.Fatal("docs/readme.txt should be unblocked after remove")
	}
}
