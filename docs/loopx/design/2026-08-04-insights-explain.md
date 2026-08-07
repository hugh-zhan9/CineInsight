# 洞察页关键查询 EXPLAIN 记录

- 生成时间：2026-08-05T08:48:55+08:00
- 库规模：视频 19416 个，总时长 14588640 秒
- 采集方式：`cmd/stats_explain` 捕获 `LibraryStatsService.GetStats()` 实际执行的 SQL 后逐条 `EXPLAIN`

## 查询 1

```sql
SELECT 
		COUNT(*) AS video_count,
		COALESCE(SUM(duration), 0) AS total_duration,
		COALESCE(SUM(size), 0) AS total_size,
		COALESCE(SUM(CASE WHEN is_watched THEN 1 ELSE 0 END), 0) AS watched_count
	 FROM "videos" WHERE "videos"."deleted_at" IS NULL
```

```text
Aggregate  (cost=2743.39..2743.40 rows=1 width=80)
  ->  Index Scan using idx_videos_score_inputs_active on videos  (cost=0.29..2534.18 rows=20920 width=16)
```

## 查询 2

```sql
SELECT directory AS label, COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes FROM "videos" WHERE "videos"."deleted_at" IS NULL GROUP BY "directory" ORDER BY bytes DESC, label ASC LIMIT 50
```

```text
Limit  (cost=2939.34..2939.46 rows=50 width=136)
  ->  Sort  (cost=2939.34..2952.91 rows=5430 width=136)
        Sort Key: (COALESCE(sum(size), '0'::numeric)) DESC, directory
        ->  HashAggregate  (cost=2691.08..2758.96 rows=5430 width=136)
              Group Key: directory
              ->  Index Scan using idx_videos_score_inputs_active on videos  (cost=0.29..2534.18 rows=20920 width=104)
```

## 查询 3

```sql
SELECT tags.name AS label, COUNT(DISTINCT videos.id) AS count, COALESCE(SUM(videos.size), 0) AS bytes FROM "tags" JOIN video_tags ON video_tags.tag_id = tags.id JOIN videos ON videos.id = video_tags.video_id AND videos.deleted_at IS NULL WHERE tags.deleted_at IS NULL GROUP BY tags.id, tags.name ORDER BY bytes DESC, tags.name ASC LIMIT 50
```

```text
Limit  (cost=2028.81..2028.93 rows=50 width=58)
  ->  Sort  (cost=2028.81..2029.35 rows=217 width=58)
        Sort Key: (COALESCE(sum(videos.size), '0'::numeric)) DESC, tags.name
        ->  GroupAggregate  (cost=0.73..2021.60 rows=217 width=58)
              Group Key: tags.id
              ->  Merge Join  (cost=0.73..1980.81 rows=5076 width=34)
                    Merge Cond: (video_tags.tag_id = tags.id)
                    ->  Nested Loop  (cost=0.58..6632.99 rows=5380 width=24)
                          ->  Index Only Scan using idx_video_tags_tag_video on video_tags  (cost=0.29..275.07 rows=12719 width=16)
                          ->  Memoize  (cost=0.30..0.58 rows=1 width=16)
                                Cache Key: video_tags.video_id
                                Cache Mode: logical
                                ->  Index Scan using videos_pkey on videos  (cost=0.29..0.57 rows=1 width=16)
                                      Index Cond: (id = video_tags.video_id)
                                      Filter: (deleted_at IS NULL)
                    ->  Index Scan using tags_pkey on tags  (cost=0.14..9.29 rows=217 width=18)
                          Filter: (deleted_at IS NULL)
```

## 查询 4

```sql
SELECT tags.name AS label, COUNT(DISTINCT videos.id) AS count, COALESCE(SUM(videos.size), 0) AS bytes FROM "tags" JOIN video_tags ON video_tags.tag_id = tags.id JOIN videos ON videos.id = video_tags.video_id AND videos.deleted_at IS NULL WHERE tags.deleted_at IS NULL AND tags.is_system = true GROUP BY tags.id, tags.name ORDER BY count DESC, tags.name ASC LIMIT 20
```

```text
Limit  (cost=505.25..505.28 rows=14 width=58)
  ->  Sort  (cost=505.25..505.28 rows=14 width=58)
        Sort Key: (count(DISTINCT videos.id)) DESC, tags.name
        ->  GroupAggregate  (cost=3.08..504.98 rows=14 width=58)
              Group Key: tags.id
              ->  Incremental Sort  (cost=3.08..502.36 rows=327 width=34)
                    Sort Key: tags.id, videos.id
                    Presorted Key: tags.id
                    ->  Nested Loop  (cost=0.72..491.77 rows=327 width=34)
                          ->  Nested Loop  (cost=0.43..52.36 rows=774 width=26)
                                ->  Index Scan using tags_pkey on tags  (cost=0.14..9.29 rows=14 width=18)
                                      Filter: ((deleted_at IS NULL) AND is_system)
                                ->  Index Only Scan using idx_video_tags_tag_video on video_tags  (cost=0.29..2.47 rows=61 width=16)
                                      Index Cond: (tag_id = tags.id)
                          ->  Index Scan using videos_pkey on videos  (cost=0.29..0.57 rows=1 width=16)
                                Index Cond: (id = video_tags.video_id)
                                Filter: (deleted_at IS NULL)
```

## 查询 5

```sql
SELECT height, COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes FROM "videos" WHERE "videos"."deleted_at" IS NULL GROUP BY "height"
```

```text
HashAggregate  (cost=2691.08..2696.97 rows=471 width=48)
  Group Key: height
  ->  Index Scan using idx_videos_score_inputs_active on videos  (cost=0.29..2534.18 rows=20920 width=16)
```

## 查询 6

```sql
SELECT CAST(last_played_at AS DATE) AS date, COUNT(*) AS count FROM "videos" WHERE last_played_at >= '2025-08-05 08:48:55.497' AND "videos"."deleted_at" IS NULL GROUP BY CAST(last_played_at AS DATE) ORDER BY date ASC
```

```text
Sort  (cost=241.14..241.87 rows=294 width=12)
  Sort Key: ((last_played_at)::date)
  ->  HashAggregate  (cost=225.41..229.08 rows=294 width=12)
        Group Key: (last_played_at)::date
        ->  Index Only Scan using idx_videos_last_played_active on videos  (cost=0.28..223.61 rows=360 width=4)
              Index Cond: (last_played_at >= '2025-08-05 08:48:55.497+00'::timestamp with time zone)
```

## 查询 7

```sql
SELECT personal_rating AS rating, COUNT(*) AS count FROM "videos" WHERE personal_rating IS NOT NULL AND "videos"."deleted_at" IS NULL GROUP BY "personal_rating" ORDER BY personal_rating ASC
```

```text
GroupAggregate  (cost=0.29..2437.42 rows=200 width=20)
  Group Key: personal_rating
  ->  Index Only Scan Backward using idx_videos_rating_active on videos  (cost=0.29..2331.35 rows=20815 width=12)
        Index Cond: (personal_rating IS NOT NULL)
```

