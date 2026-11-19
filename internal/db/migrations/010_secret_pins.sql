-- secret_pins holds the programs that the owner allowed to get a bound secret. A row covers
-- one program file with one sha256 hash. The pin of a shebang interpreter is a second row.
CREATE TABLE secret_pins (
    name       TEXT NOT NULL,     -- the exec_secrets name
    path       TEXT NOT NULL,     -- the real path of the program
    sha256     TEXT NOT NULL,     -- sha256 hex of the file
    created_at INTEGER NOT NULL,  -- Unix ms
    PRIMARY KEY (name, path)
);
