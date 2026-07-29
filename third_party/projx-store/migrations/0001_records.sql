-- +sow up
CREATE TABLE records (
    id    TEXT PRIMARY KEY,
    kind  INTEGER,
    scope INTEGER,
    rkey  TEXT,
    body  TEXT
);
