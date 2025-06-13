CREATE TABLE account (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  balance DECIMAL NOT NULL
);

-- Will automatically be split once cluster receives load but helpful if you'd like to
-- preemptively do this to ensure even distribution of ranges across the cluster.
ALTER TABLE account SPLIT AT
  SELECT rpad(to_hex(prefix::INT), 32, '0')::UUID
  FROM generate_series(0, 16) AS prefix;