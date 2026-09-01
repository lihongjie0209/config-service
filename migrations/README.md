# Migrations

Database-specific migrations live under `mysql`, `postgres`, and `kingbase`. Set `migration.path` to the matching directory, for example `migrations/postgres`. Review indexes, collation and online-DDL impact against production data before deployment.

Migration `000006` preserves every existing entry as a platform or tenant default by setting `application_id` to the empty string. New application overrides use a non-empty application ID and resolve ahead of tenant and platform defaults.
