package wire

import "testing"

func TestExitForFollowsTheDocumentedMapping(t *testing.T) {
	for name, want := range map[string]int{
		ErrBadRequest: 2, ErrUsage: 2, ErrUnavailable: 3, ErrDaemonOffline: 3, ErrNotFound: 4,
		ErrBusy: 5, ErrBehind: 5, ErrWaitTimeout: 6, ErrCursorExpired: 7,
		ErrRemovalDisabled: 8, ErrTargetExists: 8, ErrHeldByLive: 8,
		ErrInternal: 1, "something_new": 1,
	} {
		if got := ExitFor(name); got != want {
			t.Errorf("ExitFor(%s) = %d, want %d", name, got, want)
		}
	}
}

func TestHTTPStatusIsDistinctPerFamily(t *testing.T) {
	if HTTPStatus(ErrNotFound) != 404 || HTTPStatus(ErrBadRequest) != 400 || HTTPStatus(ErrInternal) != 500 ||
		HTTPStatus(ErrUnavailable) != 503 || HTTPStatus(ErrBusy) != 429 || HTTPStatus(ErrCursorExpired) != 410 ||
		HTTPStatus(ErrRemovalDisabled) != 409 {
		t.Fatal("unexpected status mapping")
	}
}
