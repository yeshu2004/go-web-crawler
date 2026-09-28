CREATE TABLE
    IF NOT EXISTS word_counts (
        crawler_id UUID NOT NULL,
        word TEXT NOT NULL,
        count BIGINT NOT NULL,
        PRIMARY KEY (crawler_id, word)
    );