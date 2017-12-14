INSERT INTO users (id, username, password, email, verified_email, tfa_code, tfa_enabled) VALUES (1, 'icook', '$2a$06$pJF0DSl6M7pTjPv8hBTP1uL/lAe7UqHZl5gKc3QA02yRFV1oCTFum', 't@test.com', false, NULL, false);
INSERT INTO payout_address (user_id, currency, address) VALUES (1, 'BTM_T', 'mucHkBoHAF8DTQWFuwQXHiewqi3ZBDNNWh');
