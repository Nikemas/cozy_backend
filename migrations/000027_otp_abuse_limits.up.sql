-- B2 fix/security: OTP abuse limits.
-- request_ip     — client IP of /otp/request, for the per-IP hourly limit.
-- verify_attempts — /otp/verify attempts spent on this code; at
--                   OTP_VERIFY_MAX_ATTEMPTS the code is burned (consumed_at).
-- created_at index — the global rolling-24h SMS ceiling counts all rows.
ALTER TABLE otp_codes ADD COLUMN request_ip TEXT;
ALTER TABLE otp_codes ADD COLUMN verify_attempts INT NOT NULL DEFAULT 0;
CREATE INDEX idx_otp_codes_created_at ON otp_codes (created_at);
CREATE INDEX idx_otp_codes_request_ip ON otp_codes (request_ip, created_at)
  WHERE request_ip IS NOT NULL;
