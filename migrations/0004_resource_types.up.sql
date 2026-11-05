-- The application validates the kind; the database keeps only its shape, so new kinds need no migration.
ALTER TABLE incidents DROP CONSTRAINT incidents_resource_type_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_resource_type_check CHECK (resource_type ~ '^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$');
