LOCK TABLE incidents IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM incidents WHERE resource_type NOT IN ('deployment', 'service', 'pod')) THEN
  RAISE EXCEPTION 'cannot downgrade with incidents for other kinds; delete them or restore a compatible backup';
 END IF;
END $$;
ALTER TABLE incidents DROP CONSTRAINT incidents_resource_type_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_resource_type_check CHECK (resource_type IN ('deployment', 'service', 'pod'));
