package engine

import "context"

// Peek asks the server whether another computer sent anything after
// *cursor, without loading this computer's sync state, which is the costly
// part of a run. It is what lets an open Helium check for the other
// computer's changes every few seconds.
//
// It reports true when a full Run is owed: a change from another computer,
// changes of this computer still waiting to be sent, or no cursor yet. A
// cursor that only passes this computer's own changes is moved on. An empty
// *cursor is read from the saved state.
func Peek(ctx context.Context, tr Transport, stateDir, deviceID string, cursor *string) (bool, error) {
	if *cursor == "" {
		st, err := LoadState(stateDir, deviceID)
		if err != nil {
			return false, err
		}
		if st.Cursor == "" || len(st.Outbox) > 0 {
			return true, nil
		}
		*cursor = st.Cursor
	}
	ops, next, err := tr.Pull(ctx, *cursor)
	if err != nil {
		return false, err
	}
	for _, op := range ops {
		if op.Stamp.Device != deviceID {
			return true, nil
		}
	}
	*cursor = next
	return false, nil
}
