DROP INDEX IF EXISTS idx_otp_codes_request_ip;
DROP INDEX IF EXISTS idx_otp_codes_created_at;
ALTER TABLE otp_codes DROP COLUMN IF EXISTS verify_attempts;
ALTER TABLE otp_codes DROP COLUMN IF EXISTS request_ip;
