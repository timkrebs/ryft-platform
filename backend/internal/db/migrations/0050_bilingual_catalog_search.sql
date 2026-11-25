-- Keep the previous call intact for older binaries while adding the secondary term.
DO $$
DECLARE extension_schema text;
BEGIN
    SELECT n.nspname INTO STRICT extension_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_trgm';
    EXECUTE replace($definition$
CREATE FUNCTION nutrition.search_foods(text,text,text,integer,integer,text)
RETURNS TABLE(id uuid,label text,brand text,food_type text,quality text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $search$
BEGIN
    RETURN QUERY EXECUTE $query$
WITH terms AS (
    SELECT plainto_tsquery('pg_catalog.simple',$1) ||
           plainto_tsquery(CASE split_part($2,'-',1) WHEN 'de' THEN 'pg_catalog.german'::regconfig
               WHEN 'en' THEN 'pg_catalog.english'::regconfig ELSE 'pg_catalog.simple'::regconfig END,$1) AS query
), candidates AS (
    SELECT DISTINCT ON (f.id) f.id,n.label,f.brand,f.food_type,f.quality,f.completeness,
           CASE WHEN n.normalized=$1 THEN 7
                WHEN n.normalized LIKE $1 || '%' THEN 6
                WHEN n.normalized_brand LIKE $1 || '%' THEN 5
                WHEN n.document @@ terms.query THEN 4
                WHEN f.food_type='generic' AND $6<>'' AND n.normalized=$6 THEN 3
                WHEN f.food_type='generic' AND length($6)>=3 AND n.normalized LIKE $6 || '%' THEN 2
                ELSE 1 END AS match,
           CASE WHEN n.language=$2 THEN 2 WHEN n.language=split_part($2,'-',1) THEN 1 ELSE 0 END AS language_rank,
           NOT n.alias AS preferred_label,ts_rank(n.document,terms.query) AS lexical,
           @trgm@.similarity(n.normalized,$1) AS fuzzy,$3=ANY(f.markets) AS preferred_market
    FROM nutrition.food_names n JOIN nutrition.foods f ON f.id=n.food_id CROSS JOIN terms
    WHERE f.status='active' AND nutrition.account() IS NOT NULL
      AND (f.owner_user_id IS NULL OR f.owner_user_id=nutrition.account())
      AND (n.normalized LIKE $1 || '%' OR n.normalized_brand LIKE $1 || '%' OR n.document @@ terms.query
           OR (length($1)>=3 AND n.normalized OPERATOR(@trgm@.%) $1)
           OR (f.food_type='generic' AND $6<>'' AND
              (n.normalized=$6 OR (length($6)>=3 AND n.normalized LIKE $6 || '%'))))
    ORDER BY f.id,match DESC,language_rank DESC,preferred_label DESC,lexical DESC,fuzzy DESC,n.label
)
SELECT id,label,brand,food_type,quality FROM candidates
ORDER BY match DESC,preferred_market DESC,language_rank DESC,
    CASE quality WHEN 'verified' THEN 3 WHEN 'source_reported' THEN 2 WHEN 'user_reported' THEN 1 ELSE 0 END DESC,
    completeness DESC,lexical DESC,fuzzy DESC,id
LIMIT least(greatest($4,0),51) OFFSET least(greatest($5,0),1000)
    $query$ USING $1,$2,$3,$4,$5,$6;
END;
$search$
    $definition$, '@trgm@', quote_ident(extension_schema));
END $$;
REVOKE ALL ON FUNCTION nutrition.search_foods(text,text,text,integer,integer,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.search_foods(text,text,text,integer,integer,text) TO ryft_api;
