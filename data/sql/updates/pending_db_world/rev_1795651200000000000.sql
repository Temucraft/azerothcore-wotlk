-- Temucraft: `command`.`security` holds an RBAC id when it is 200 or more (ChatCommand.cpp), and many RBAC ids are
-- above 255, so tinyint can't hold them. The loader reads it as uint16 to match.
ALTER TABLE `command` MODIFY `security` SMALLINT UNSIGNED NOT NULL DEFAULT 0;
