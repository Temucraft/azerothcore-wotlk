-- Temucraft: `command`.`security` holds an RBAC id when it is 200 or more (ChatCommand.cpp). SMALLINT (rev_1795651200000000000)
-- still stops at 65535, and the module permissions are 103250-104351 (mod-temucraft-loadouts' 104351 for .loadout),
-- so they were cut to 65535 (or refused in strict mode). INT holds every RBAC id; the loader reads it as uint32.
ALTER TABLE `command` MODIFY `security` INT UNSIGNED NOT NULL DEFAULT 0;
