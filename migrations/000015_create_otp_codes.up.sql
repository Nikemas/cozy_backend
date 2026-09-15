-- Код и его проверка живут на стороне Nikita SMSPro OTP API; здесь храним
-- только транзакцию (token), чтобы связать /otp/request и /otp/verify.
CREATE TABLE otp_codes (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  phone          TEXT NOT NULL,
  transaction_id TEXT NOT NULL UNIQUE,
  token          TEXT NOT NULL,
  expires_at     TIMESTAMPTZ NOT NULL,
  consumed_at    TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_otp_codes_phone ON otp_codes(phone);
