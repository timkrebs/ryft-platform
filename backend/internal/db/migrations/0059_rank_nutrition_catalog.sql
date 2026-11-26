-- D284: Personalized, group-aware catalog search. Older API binaries keep their original
-- search functions for rollback.
--
-- A query is "broad" when it names a canonical staple group ("milch" or "milk" for group
-- milk, "apfel" or "apple" for group apple) or is one word of at least four characters that
-- a staple's name or alias starts with. Exact equality with a variant alias ("Milch 1,5
-- Prozent") is a specific query and does not promote the whole group.
--
-- Tiers, highest first (favorites, 90-day uses and last use then order within a tier):
--   10 staple of a promoted group       9 whole forms of a promoted group
--    7 exact name match                 6 cultivar of a promoted group
--    5 other forms of a promoted group (juice, ...), above brand and prefix matches
--    4 prefix, brand-prefix and text matches    2 alias fallback    1 fuzzy
DO $$
DECLARE extension_schema text;
BEGIN
    SELECT n.nspname INTO STRICT extension_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_trgm';
    EXECUTE replace($definition$
CREATE OR REPLACE FUNCTION nutrition.search_foods_v2(text,text,text,integer,integer,text)
RETURNS TABLE(id uuid,label text,brand text,food_type text,quality text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $search$
BEGIN
    RETURN QUERY EXECUTE $query$
WITH scope AS (SELECT nutrition.account() AS user_id), terms AS (
    SELECT plainto_tsquery('pg_catalog.simple',$1) ||
           plainto_tsquery(CASE split_part($2,'-',1) WHEN 'de' THEN 'pg_catalog.german'::regconfig
               WHEN 'en' THEN 'pg_catalog.english'::regconfig ELSE 'pg_catalog.simple'::regconfig END,$1) AS query,
           CASE WHEN strpos($6,' ')>0 THEN plainto_tsquery('pg_catalog.english',$6) ||
               plainto_tsquery('pg_catalog.german',$6) || plainto_tsquery('pg_catalog.simple',$6) END AS alternate
), candidates AS MATERIALIZED (
    SELECT DISTINCT ON (f.id) f.id,n.label,f.brand,f.food_type,f.quality,f.completeness,
           f.is_canonical_staple,f.canonical_group_key,f.food_form,
           CASE WHEN n.normalized=$1 THEN 7
                WHEN n.normalized LIKE $1 || '%' THEN 6
                WHEN n.normalized_brand LIKE $1 || '%' THEN 5
                WHEN n.document @@ terms.query THEN 4
                WHEN f.food_type='generic' AND $6<>'' AND n.normalized=$6 THEN 3
                WHEN f.food_type='generic' AND ((length($6)>=3 AND n.normalized LIKE $6 || '%') OR
                    (strpos($6,' ')>0 AND n.document @@ terms.alternate)) THEN 2
                ELSE 1 END AS match,
           CASE WHEN n.language=$2 THEN 2 WHEN n.language=split_part($2,'-',1) THEN 1 ELSE 0 END AS language_rank,
           NOT n.alias AS preferred_label,ts_rank(n.document,terms.query) AS lexical,
           @trgm@.similarity(n.normalized,$1) AS fuzzy,$3=ANY(f.markets) AS preferred_market,
           (f.is_canonical_staple AND (translate(f.canonical_group_key,'_-','  ') IN ($1,$6) OR
                (length($1)>=4 AND strpos($1,' ')=0 AND n.normalized LIKE $1 || '%'))) AS broad_match
    FROM nutrition.food_names n JOIN nutrition.foods f ON f.id=n.food_id CROSS JOIN terms CROSS JOIN scope
    WHERE f.status='active' AND scope.user_id IS NOT NULL
      AND (f.owner_user_id IS NULL OR f.owner_user_id=scope.user_id)
      AND (n.normalized LIKE $1 || '%' OR n.normalized_brand LIKE $1 || '%' OR n.document @@ terms.query
           OR (length($1)>=3 AND n.normalized OPERATOR(@trgm@.%) $1)
           OR (f.food_type='generic' AND $6<>'' AND
              (n.normalized=$6 OR (length($6)>=3 AND n.normalized LIKE $6 || '%') OR
               (strpos($6,' ')>0 AND n.document @@ terms.alternate))))
    ORDER BY f.id,match DESC,language_rank DESC,preferred_label DESC,lexical DESC,fuzzy DESC,n.label
), staple_groups AS (
    SELECT DISTINCT canonical_group_key FROM candidates WHERE is_canonical_staple AND broad_match
), recent AS (
    SELECT i.food_id,count(*) AS uses,max(i.created_at) AS last_used
    FROM nutrition.meal_items i JOIN scope ON i.user_id=scope.user_id
    WHERE i.food_id IN (SELECT id FROM candidates) AND i.created_at>=statement_timestamp()-interval '90 days'
    GROUP BY i.food_id
)
SELECT c.id,c.label,c.brand,c.food_type,c.quality FROM candidates c CROSS JOIN scope
LEFT JOIN staple_groups USING (canonical_group_key)
LEFT JOIN nutrition.food_favorites fav ON fav.user_id=scope.user_id AND fav.food_id=c.id
LEFT JOIN recent ON recent.food_id=c.id
ORDER BY CASE WHEN staple_groups.canonical_group_key IS NOT NULL AND c.is_canonical_staple THEN 10
              WHEN staple_groups.canonical_group_key IS NOT NULL AND c.food_form IN ('whole','raw','whole_raw','whole_food') THEN 9
              WHEN c.match=7 THEN 7
              WHEN staple_groups.canonical_group_key IS NOT NULL AND c.food_form='cultivar' THEN 6
              WHEN staple_groups.canonical_group_key IS NOT NULL AND c.food_form IS NOT NULL THEN 5
              WHEN c.match>=4 THEN 4 WHEN c.match>=2 THEN 2 ELSE 1 END DESC,
    (fav.food_id IS NOT NULL) DESC, recent.uses DESC NULLS LAST, recent.last_used DESC NULLS LAST,
    c.match DESC,c.preferred_market DESC,c.language_rank DESC,
    CASE c.quality WHEN 'verified' THEN 3 WHEN 'source_reported' THEN 2 WHEN 'user_reported' THEN 1 ELSE 0 END DESC,
    c.completeness DESC,c.lexical DESC,c.fuzzy DESC,c.id
LIMIT least(greatest($4,0),51) OFFSET least(greatest($5,0),1000)
    $query$ USING $1,$2,$3,$4,$5,$6;
END;
$search$
    $definition$, '@trgm@', quote_ident(extension_schema));
END $$;
REVOKE ALL ON FUNCTION nutrition.search_foods_v2(text,text,text,integer,integer,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.search_foods_v2(text,text,text,integer,integer,text) TO ryft_api;
