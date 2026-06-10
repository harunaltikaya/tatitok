-- message-table DDL captured verbatim from the live opencode.db
-- (public drizzle-generated structure; the ONLY table ccusage
-- opencode needs — verified empirically). Go tests reconstruct
-- the fixture db from this DDL + messages/*.jsonl.
CREATE TABLE `message` (
	`id` text PRIMARY KEY,
	`session_id` text NOT NULL,
	`time_created` integer NOT NULL,
	`time_updated` integer NOT NULL,
	`data` text NOT NULL,
	CONSTRAINT `fk_message_session_id_session_id_fk` FOREIGN KEY (`session_id`) REFERENCES `session`(`id`) ON DELETE CASCADE
);
