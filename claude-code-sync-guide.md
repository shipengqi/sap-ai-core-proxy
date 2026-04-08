# Claude Code 配置同步指南（Mac → WSL）

> Mac 是主力机，WSL 是辅助机。Mac 用户名与 WSL（pooky）不同。

---

## 第一部分：Mac 上执行

### 1. 固定 memory 数据路径

```bash
mkdir -p ~/.claude/memory

# 添加到 shell 配置
echo 'export MEMORY_FILE_PATH="$HOME/.claude/memory/memory.jsonl"' >> ~/.zshrc
source ~/.zshrc
```

设置完后**重启 Claude Code**，让 memory server 使用新路径。

---

### 2. 创建 .gitignore

```bash
cat > ~/.claude/.gitignore << 'GITIGNORE'
# 插件缓存（~101MB，会自动下载）
plugins/cache/
plugins/data/
plugins/install-counts-cache.json

# 会话和临时数据（机器特定）
sessions/
session-data/
session-env/
file-history/
plans/
shell-snapshots/
telemetry/
metrics/
backups/
ide/
homunculus/
.agents/
projects/

# 日志
*.log

# 敏感文件
.credentials.json

# OS
.DS_Store
Thumbs.db
GITIGNORE
```

---

### 3. 创建路径适配脚本

```bash
mkdir -p ~/.claude/scripts

cat > ~/.claude/scripts/sync-paths.sh << 'SCRIPT'
#!/bin/bash
# 在 git pull 后运行，将 settings.json 中的 home 路径适配到当前机器
set -e

SETTINGS="$HOME/.claude/settings.json"
if [[ ! -f "$SETTINGS" ]]; then
  echo "ERROR: settings.json not found at $SETTINGS"
  exit 1
fi

echo "Adapting paths to HOME=$HOME ..."

if [[ "$OSTYPE" == "darwin"* ]]; then
  # macOS
  sed -i '' -E "s|(\"[^\"]*)/home/[^/]+/\.claude/|\1$HOME/.claude/|g" "$SETTINGS"
  sed -i '' -E "s|(\"[^\"]*)/Users/[^/]+/\.claude/|\1$HOME/.claude/|g" "$SETTINGS"
else
  # Linux / WSL
  sed -i -E "s|(\"[^\"]*)/home/[^/]+/\.claude/|\1$HOME/.claude/|g" "$SETTINGS"
  sed -i -E "s|(\"[^\"]*)/Users/[^/]+/\.claude/|\1$HOME/.claude/|g" "$SETTINGS"
fi

echo "Done. All paths now point to $HOME/.claude/"
SCRIPT

chmod +x ~/.claude/scripts/sync-paths.sh
```

---

### 4. 初始化 Git 仓库并推送

```bash
cd ~/.claude
git init
git add -A
git commit -m "feat: initial claude code config from mac"

# 创建 GitHub 私有仓库并推送（二选一）

# 方式一：gh CLI（推荐）
gh repo create claude-code-config --private --source=. --push

# 方式二：手动
# git remote add origin git@github.com:YOUR_USER/claude-code-config.git
# git branch -M main
# git push -u origin main
```

---

### 5. 添加日常同步别名

```bash
cat >> ~/.zshrc << 'ALIASES'

# Claude Code 配置同步
alias claude-push='cd ~/.claude && git add -A && git diff --cached --quiet && echo "No changes to sync" || (git commit -m "sync: $(date +%Y-%m-%d) from $(hostname)" && git push) && cd -'
alias claude-pull='cd ~/.claude && git pull && bash scripts/sync-paths.sh && cd -'
ALIASES

source ~/.zshrc
```

---

## 第二部分：WSL 上执行

### 1. 克隆仓库

```bash
# WSL 是全新的，直接克隆
git clone git@github.com:YOUR_USER/claude-code-config.git ~/.claude
```

---

### 2. 适配路径

```bash
bash ~/.claude/scripts/sync-paths.sh
```

这会把 `settings.json` 中所有 `/Users/xxx/.claude/` 替换为 `/home/pooky/.claude/`。

---

### 3. 创建被 .gitignore 排除的目录

```bash
mkdir -p ~/.claude/{sessions,session-data,session-env,plugins/cache,plugins/data,plans,file-history,telemetry,metrics,backups,ide,homunculus,.agents,projects,shell-snapshots}
```

---

### 4. 设置 memory 路径

```bash
echo 'export MEMORY_FILE_PATH="$HOME/.claude/memory/memory.jsonl"' >> ~/.bashrc
source ~/.bashrc
```

---

### 5. 安装 ECC 插件

```bash
claude plugins install ecc@ecc
```

> `plugins/cache/` 被排除了，所以需要在 WSL 上重新安装一次。
> 插件配置（`plugin.json`、`marketplace.json`、`installed_plugins.json`）已经同步过来了，
> 只需要下载插件本体。

---

### 6. 添加日常同步别名

```bash
cat >> ~/.bashrc << 'ALIASES'

# Claude Code 配置同步
alias claude-push='cd ~/.claude && git add -A && git diff --cached --quiet && echo "No changes to sync" || (git commit -m "sync: $(date +%Y-%m-%d) from $(hostname)" && git push) && cd -'
alias claude-pull='cd ~/.claude && git pull && bash scripts/sync-paths.sh && cd -'
ALIASES

source ~/.bashrc
```

---

## 日常使用

```
Mac 上工作一天  →  claude-push
WSL 上开始工作  →  claude-pull
WSL 上改了配置  →  claude-push（可选，想同步回 Mac 时）
Mac 上继续工作  →  claude-pull（可选，WSL 有改动时）
```

---

## 同步内容清单

| 内容 | 路径 | 说明 |
|------|------|------|
| 全局设置 | `settings.json` | hooks、env、插件开关 |
| 插件配置 | `plugin.json`、`marketplace.json` | 插件注册信息 |
| 规则 | `rules/` | 编码风格、安全、测试等规则 |
| 代理 | `agents/` | 代理定义文件 |
| 技能 | `skills/` | 所有 skill 文件 |
| 命令 | `commands/` | 自定义命令 |
| 钩子 | `hooks/` | 钩子脚本 |
| 辅助脚本 | `scripts/` | 包括路径适配脚本 |
| MCP 配置 | `mcp-configs/` | MCP 服务器定义 |
| ECC 配置 | `ecc/` | ECC 插件配置 |
| **记忆数据** | `memory/memory.jsonl` | 知识图谱，跨会话记忆 |

---

## 注意事项

1. **API Key 不要写入 Git** — `mcp-configs/mcp-servers.json` 中的 key 都是 `YOUR_*_HERE` 占位符，实际值通过各机器的环境变量设置
2. **凭据文件** — `.credentials.json` 已被 `.gitignore` 排除，每台机器单独配置
3. **记忆冲突** — 如果两台机器同时积累了不同记忆，JSONL 格式大多数情况能自动合并。冲突时以 Mac 为准：`git checkout --theirs memory/memory.jsonl`
4. **Stop hooks** — 内联 node 代码已通过 `require('os').homedir()` 动态解析路径，不需要 `sync-paths.sh` 处理
5. **PreToolUse/PostToolUse hooks** — 使用硬编码绝对路径，`sync-paths.sh` 会自动适配

---

## 验证（WSL 上同步完成后）

```bash
# 1. 检查路径是否正确（应该看到 /home/pooky/.claude/）
grep -o '"/home/[^"]*\.claude/' ~/.claude/settings.json | head -3

# 2. 检查 memory 路径
echo $MEMORY_FILE_PATH

# 3. 启动 Claude Code 看是否正常
claude

# 4. 检查 skills 是否可用
ls ~/.claude/skills/

# 5. 检查 MCP 配置
cat ~/.claude/mcp-configs/mcp-servers.json | head -5
```
