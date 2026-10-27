CREATE TABLE users (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    name          VARCHAR(255) NOT NULL,
    created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE teams (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255)    NOT NULL,
    created_by BIGINT UNSIGNED NOT NULL,
    created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (created_by) REFERENCES users (id)
);

CREATE TABLE team_members (
    team_id BIGINT UNSIGNED                  NOT NULL,
    user_id BIGINT UNSIGNED                  NOT NULL,
    role    ENUM ('owner', 'admin', 'member') NOT NULL,
    PRIMARY KEY (team_id, user_id),
    FOREIGN KEY (team_id) REFERENCES teams (id),
    FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE TABLE tasks (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    team_id     BIGINT UNSIGNED                      NOT NULL,
    title       VARCHAR(255)                         NOT NULL,
    description TEXT                                 NOT NULL,
    status      ENUM ('todo', 'in_progress', 'done') NOT NULL DEFAULT 'todo',
    created_by  BIGINT UNSIGNED                      NOT NULL,
    assignee_id BIGINT UNSIGNED                      NULL,
    created_at  DATETIME                             NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME                             NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at   DATETIME                             NULL,
    version     INT                                  NOT NULL DEFAULT 1,
    INDEX idx_tasks_team_status_assignee (team_id, status, assignee_id),
    FOREIGN KEY (team_id) REFERENCES teams (id),
    FOREIGN KEY (created_by) REFERENCES users (id),
    FOREIGN KEY (assignee_id) REFERENCES users (id)
);

CREATE TABLE task_history (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id    BIGINT UNSIGNED NOT NULL,
    changed_by BIGINT UNSIGNED NOT NULL,
    changes    JSON            NOT NULL,
    created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (task_id) REFERENCES tasks (id),
    FOREIGN KEY (changed_by) REFERENCES users (id)
);

CREATE TABLE task_comments (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id    BIGINT UNSIGNED NOT NULL,
    user_id    BIGINT UNSIGNED NOT NULL,
    content    TEXT            NOT NULL,
    created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (task_id) REFERENCES tasks (id),
    FOREIGN KEY (user_id) REFERENCES users (id)
);
