package keystore

// declaration retains only the configured bindings. Canonical enrollments are
// additional registry rows and never alter the interoperation fingerprint.
const identitySchema = `
CREATE TABLE IF NOT EXISTS identity_mode (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 organization TEXT NOT NULL DEFAULT '',
 required INTEGER NOT NULL DEFAULT 0 CHECK(required IN (0,1)),
 fingerprint TEXT NOT NULL DEFAULT '',
 declaration TEXT NOT NULL DEFAULT ''
);
INSERT INTO identity_mode(singleton) SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM identity_mode WHERE singleton=1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS identity_registry (
 organization TEXT NOT NULL CHECK(length(organization)>0),
 kind TEXT NOT NULL CHECK(kind IN ('human','service')),
 issuer TEXT NOT NULL CHECK(length(issuer)>0),
 subject TEXT NOT NULL CHECK(length(subject)>0),
 account_ref TEXT NOT NULL CHECK(length(account_ref)>0),
 PRIMARY KEY(organization,kind,issuer,subject),
 UNIQUE(organization,account_ref)
);`

const sqliteIdentityGuards = `
CREATE TRIGGER IF NOT EXISTS identity_registry_insert_guard
BEFORE INSERT ON identity_registry
WHEN NEW.organization<>(SELECT organization FROM identity_mode WHERE singleton=1)
BEGIN SELECT RAISE(ABORT,'keystore identity organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS identity_registry_bijection_insert_guard
BEFORE INSERT ON identity_registry
WHEN EXISTS(SELECT 1 FROM identity_registry i WHERE i.organization=NEW.organization AND
 (i.account_ref=NEW.account_ref OR (i.kind=NEW.kind AND i.issuer=NEW.issuer AND i.subject=NEW.subject)) AND
 NOT (i.account_ref=NEW.account_ref AND i.kind=NEW.kind AND i.issuer=NEW.issuer AND i.subject=NEW.subject))
BEGIN SELECT RAISE(ABORT,'keystore identity binding is immutable'); END;
CREATE TRIGGER IF NOT EXISTS identity_registry_update_guard
BEFORE UPDATE ON identity_registry
BEGIN SELECT RAISE(ABORT,'keystore identity binding is immutable'); END;
CREATE TRIGGER IF NOT EXISTS identity_registry_delete_guard
BEFORE DELETE ON identity_registry
BEGIN SELECT RAISE(ABORT,'keystore identity binding is immutable'); END;
CREATE TRIGGER IF NOT EXISTS identity_mode_update_guard
BEFORE UPDATE ON identity_mode
WHEN (OLD.organization<>'' AND NEW.organization<>OLD.organization) OR
 (OLD.required=1 AND (NEW.required<>1 OR NEW.fingerprint<>OLD.fingerprint OR NEW.declaration<>OLD.declaration))
BEGIN SELECT RAISE(ABORT,'keystore identity mode cannot downgrade'); END;
CREATE TRIGGER IF NOT EXISTS identity_mode_delete_guard
BEFORE DELETE ON identity_mode
BEGIN SELECT RAISE(ABORT,'keystore identity mode cannot be removed'); END;
CREATE TRIGGER IF NOT EXISTS identity_mode_insert_guard
BEFORE INSERT ON identity_mode
WHEN EXISTS(SELECT 1 FROM identity_mode m WHERE m.singleton=NEW.singleton AND
 ((m.organization<>'' AND NEW.organization<>m.organization) OR
 (m.required=1 AND (NEW.required<>1 OR NEW.fingerprint<>m.fingerprint OR NEW.declaration<>m.declaration))))
BEGIN SELECT RAISE(ABORT,'keystore identity mode cannot downgrade'); END;
DROP TRIGGER IF EXISTS keys_identity_insert_guard;
CREATE TRIGGER keys_identity_insert_guard
BEFORE INSERT ON keys
WHEN (SELECT required FROM identity_mode WHERE singleton=1)=1 AND (
 EXISTS(SELECT 1 FROM keys k WHERE (k.key_id=NEW.key_id OR k.key_hash=NEW.key_hash) AND k.revoked<>0 AND
  (NEW.revoked=0 OR NEW.key_id<>k.key_id OR NEW.key_hash<>k.key_hash)) OR
 ((NEW.revoked=0 OR NEW.owner<>'') AND NOT EXISTS(
 SELECT 1 FROM identity_registry i WHERE i.organization=NEW.identity_organization AND
 i.kind=NEW.identity_kind AND i.issuer=NEW.identity_issuer AND i.subject=NEW.identity_subject AND i.account_ref=NEW.owner)))
BEGIN SELECT RAISE(ABORT,'keystore identity write refused'); END;
DROP TRIGGER IF EXISTS keys_identity_update_guard;
CREATE TRIGGER keys_identity_update_guard
BEFORE UPDATE ON keys
WHEN (SELECT required FROM identity_mode WHERE singleton=1)=1 AND (
 (OLD.revoked<>0 AND (NEW.revoked=0 OR NEW.key_id<>OLD.key_id OR NEW.key_hash<>OLD.key_hash)) OR
 NEW.owner<>OLD.owner OR NEW.identity_organization<>OLD.identity_organization OR
 NEW.identity_kind<>OLD.identity_kind OR NEW.identity_issuer<>OLD.identity_issuer OR
 NEW.identity_subject<>OLD.identity_subject OR
 ((NEW.revoked=0 OR NEW.owner<>'') AND NOT EXISTS(SELECT 1 FROM identity_registry i WHERE
 i.organization=NEW.identity_organization AND i.kind=NEW.identity_kind AND
 i.issuer=NEW.identity_issuer AND i.subject=NEW.identity_subject AND i.account_ref=NEW.owner)))
BEGIN SELECT RAISE(ABORT,'keystore identity write refused'); END;
`

const postgresIdentityGuards = `
CREATE OR REPLACE FUNCTION keystore_registry_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  RAISE EXCEPTION 'keystore identity binding is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.organization<>(SELECT organization FROM identity_mode WHERE singleton=1) THEN
  RAISE EXCEPTION 'keystore identity organization mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS identity_registry_guard ON identity_registry;
CREATE TRIGGER identity_registry_guard BEFORE INSERT OR UPDATE OR DELETE ON identity_registry
FOR EACH ROW EXECUTE FUNCTION keystore_registry_guard();
CREATE OR REPLACE FUNCTION keystore_mode_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'keystore identity mode cannot be removed' USING ERRCODE='23514';
 END IF;
 IF (OLD.organization<>'' AND NEW.organization<>OLD.organization) OR
 (OLD.required=1 AND (NEW.required<>1 OR NEW.fingerprint<>OLD.fingerprint OR NEW.declaration<>OLD.declaration)) THEN
  RAISE EXCEPTION 'keystore identity mode cannot downgrade' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS identity_mode_guard ON identity_mode;
CREATE TRIGGER identity_mode_guard BEFORE UPDATE OR DELETE ON identity_mode
FOR EACH ROW EXECUTE FUNCTION keystore_mode_guard();
CREATE OR REPLACE FUNCTION keystore_key_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE identity_required INTEGER;
BEGIN
 -- Locking reads reject stale repeatable-read snapshots across activation.
 SELECT required INTO STRICT identity_required FROM identity_mode WHERE singleton=1 FOR SHARE;
 IF identity_required=1 THEN
   IF TG_OP='UPDATE' AND ((OLD.revoked<>0 AND
    (NEW.revoked=0 OR NEW.key_id<>OLD.key_id OR NEW.key_hash<>OLD.key_hash)) OR NEW.owner<>OLD.owner OR
    NEW.identity_organization<>OLD.identity_organization OR NEW.identity_kind<>OLD.identity_kind OR
   NEW.identity_issuer<>OLD.identity_issuer OR NEW.identity_subject<>OLD.identity_subject) THEN
   RAISE EXCEPTION 'keystore identity write refused' USING ERRCODE='23514';
   END IF;
   IF TG_OP='INSERT' AND EXISTS(SELECT 1 FROM keys k WHERE
    (k.key_id=NEW.key_id OR k.key_hash=NEW.key_hash) AND k.revoked<>0 AND
    (NEW.revoked=0 OR NEW.key_id<>k.key_id OR NEW.key_hash<>k.key_hash)) THEN
    RAISE EXCEPTION 'keystore identity write refused' USING ERRCODE='23514';
   END IF;
  IF (NEW.revoked=0 OR NEW.owner<>'') AND NOT EXISTS(SELECT 1 FROM identity_registry i WHERE
   i.organization=NEW.identity_organization AND i.kind=NEW.identity_kind AND
   i.issuer=NEW.identity_issuer AND i.subject=NEW.identity_subject AND i.account_ref=NEW.owner) THEN
   RAISE EXCEPTION 'keystore identity write refused' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS keys_identity_guard ON keys;
CREATE TRIGGER keys_identity_guard BEFORE INSERT OR UPDATE ON keys
FOR EACH ROW EXECUTE FUNCTION keystore_key_identity_guard();
`
