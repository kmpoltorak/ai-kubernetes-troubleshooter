LOCK TABLE reports IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM reports WHERE confidence IS NULL OR analysis->>'schema_version'='2') THEN
  RAISE EXCEPTION 'cannot downgrade with v2 reports; restore a compatible backup or use an explicitly reviewed migration';
 END IF;
END $$;
ALTER TABLE reports ALTER COLUMN confidence SET NOT NULL;
