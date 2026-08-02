-- 00-preflight-source.sql — se ejecuta contra el ORIGEN (BD de wApp).
--
-- Comprueba la única condición que, según el design.md §Migración de datos punto 2
-- del Plan 003 de identity, ABORTA la migración: dos usuarios vivos con el mismo
-- email. Es un error de DATOS, no un caso de negocio, y `iam.legacy_user_map` no
-- es el lugar donde taparlo.
--
-- Devuelve una sola fila con los conteos. El script aborta si duplicados > 0.

SELECT count(*)                                              AS total,
       count(*) FILTER (WHERE deleted_at IS NULL)            AS vivos,
       count(*) FILTER (WHERE deleted_at IS NOT NULL)        AS soft_deleted,
       (SELECT count(*)
          FROM (SELECT lower(trim(email))
                  FROM public.iam_users
                 WHERE deleted_at IS NULL
                 GROUP BY 1
                HAVING count(*) > 1) d)                      AS emails_duplicados
FROM public.iam_users;
