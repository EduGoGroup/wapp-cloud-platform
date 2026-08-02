-- 03-verify-target.sql — se ejecuta contra el DESTINO (identity, schema iam).
--
-- Fotografía de lo que quedó escrito. El script compara estos conteos con los
-- del origen y falla si no cuadran.

SELECT (SELECT count(*) FROM iam.users)                                   AS users,
       (SELECT count(*) FROM iam.api_keys WHERE ecosystem_key = 'wapp')   AS api_keys_wapp,
       (SELECT count(*) FROM iam.api_keys)                                AS api_keys_total,
       (SELECT count(*) FROM iam.legacy_user_map WHERE ecosystem = 'wapp') AS legacy_map_wapp;
