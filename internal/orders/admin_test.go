package orders

import "testing"

// TestValidStatusTransition is a table-driven test covering every
// from/to combination of OrderStatus, per the state machine documented on
// validStatusTransition:
//
//	placed           -> confirmed, cancelled
//	confirmed        -> courier_assigned, cancelled
//	courier_assigned -> delivered, cancelled
//	delivered        -> (terminal)
//	cancelled        -> (terminal)
func TestValidStatusTransition(t *testing.T) {
	allStatuses := []OrderStatus{
		StatusPlaced, StatusConfirmed, StatusCourierAssigned, StatusDelivered, StatusCancelled,
	}

	// valid lists every (from, to) pair that must return true; every other
	// combination in the cross product of allStatuses x allStatuses
	// (including same-status "transitions") must return false.
	valid := map[[2]OrderStatus]bool{
		{StatusPlaced, StatusConfirmed}:          true,
		{StatusPlaced, StatusCancelled}:          true,
		{StatusConfirmed, StatusCourierAssigned}: true,
		{StatusConfirmed, StatusCancelled}:       true,
		{StatusCourierAssigned, StatusDelivered}: true,
		{StatusCourierAssigned, StatusCancelled}: true,
	}

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := valid[[2]OrderStatus{from, to}]
			got := validStatusTransition(from, to)
			if got != want {
				t.Errorf("validStatusTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

// TestValidStatusTransitionUnknownStatus checks that an unrecognized
// "to" value (e.g. a client typo) is always rejected regardless of from,
// and that an unrecognized "from" value never opens a transition.
func TestValidStatusTransitionUnknownStatus(t *testing.T) {
	const bogus OrderStatus = "bogus_status"

	for _, from := range []OrderStatus{StatusPlaced, StatusConfirmed, StatusCourierAssigned, StatusDelivered, StatusCancelled} {
		if validStatusTransition(from, bogus) {
			t.Errorf("validStatusTransition(%q, bogus) = true, want false", from)
		}
	}
	for _, to := range []OrderStatus{StatusPlaced, StatusConfirmed, StatusCourierAssigned, StatusDelivered, StatusCancelled} {
		if validStatusTransition(bogus, to) {
			t.Errorf("validStatusTransition(bogus, %q) = true, want false", to)
		}
	}
}

func TestAdminSafeOffset(t *testing.T) {
	tests := []struct {
		page int
		want int
	}{
		{page: 0, want: 0},
		{page: 1, want: 0},
		{page: 2, want: AdminPageSize},
		{page: 3, want: 2 * AdminPageSize},
	}
	for _, tt := range tests {
		if got := adminSafeOffset(tt.page); got != tt.want {
			t.Errorf("adminSafeOffset(%d) = %d, want %d", tt.page, got, tt.want)
		}
	}
}

func TestAdminSafeOffsetDoesNotOverflow(t *testing.T) {
	// A pathologically large page must not wrap around to a negative
	// offset — Postgres rejects a negative OFFSET outright.
	if got := adminSafeOffset(1 << 30); got < 0 {
		t.Errorf("adminSafeOffset(1<<30) = %d, want non-negative", got)
	}
}
