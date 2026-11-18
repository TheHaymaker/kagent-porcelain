-- Workbench database provisioning, run once by a database administrator
-- alongside kagent's own provisioning, before the first install.
--
-- Workbench shares kagent's database and its two logins. kagent's owner role
-- applies Workbench's migrations (they run as kagent "ExtraMigrations"), and
-- kagent's read/write role serves Workbench's queries. Replace the role names
-- if you changed config.database.owner.role / readWrite.role.

CREATE SCHEMA IF NOT EXISTS workbench AUTHORIZATION kagent_owner;
GRANT USAGE ON SCHEMA workbench TO kagent_readwrite;

-- Tables the owner creates from now on are readable and writable by the
-- read/write role...
ALTER DEFAULT PRIVILEGES FOR ROLE kagent_owner IN SCHEMA workbench
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO kagent_readwrite;
ALTER DEFAULT PRIVILEGES FOR ROLE kagent_owner IN SCHEMA workbench
    GRANT USAGE, SELECT ON SEQUENCES TO kagent_readwrite;

-- ...except the audit trail, which the application may only append to. A
-- trigger refuses UPDATE and DELETE as well; this makes the refusal a
-- privilege and not only a trigger the owner could drop.
-- Run this part after the first migration has created the table:
--   REVOKE UPDATE, DELETE, TRUNCATE ON workbench.audit_event FROM kagent_readwrite;
