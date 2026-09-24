-- B2 fix/security: refresh-token reuse detection + customer account deletion.
--
-- family_id  — every login starts a family; each rotation issues the next
--              token in the same family. Presenting an already-rotated token
--              (reuse = likely theft) revokes the whole family.
-- rotated_at — set when a token was exchanged for a successor, which is what
--              tells reuse apart from a token revoked by logout.
ALTER TABLE refresh_tokens ADD COLUMN family_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE refresh_tokens ADD COLUMN rotated_at TIMESTAMPTZ;
CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family_id);

-- DELETE /api/v1/customer anonymizes the row (phone -> 'deleted:<id>', name
-- -> NULL) instead of deleting it, because orders keep referencing it.
ALTER TABLE customers ADD COLUMN deleted_at TIMESTAMPTZ;
