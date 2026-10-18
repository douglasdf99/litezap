package store

import "context"

// SetChatName names an existing chat (group subjects, marco 8); it reports whether the
// chat exists. An empty name is ignored.
func (s *Store) SetChatName(ctx context.Context, instanceID, jid, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET name = ? WHERE instance_id = ? AND jid = ?`, name, instanceID, jid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
