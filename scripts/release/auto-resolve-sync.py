#!/usr/bin/env python3
"""auto-resolve：workflow 文件一律取本仓库版本；基线写进普通数据文件。

内置 GITHUB_TOKEN 不能推送包含 `.github/workflows/*` 改动的提交（要
`workflows` 权限，App token 拿不到），所以自动同步的合并提交一旦带上上游的
workflow 改动，push 必被拒。上游几乎每次动 release.yml 都只是为了把
upstream_baseline 往前推，那部分信息现在已经挪到
scripts/release/upstream-baseline 这个普通文件里了，workflow 本身没有必须
跟随上游的理由。

因此规则改为：
  - `.github/workflows/*` 一律取本仓库版本（--ours），并在合并后把上游
    release.yml 里的 upstream_baseline 解析出来写进数据文件。
  - 其它机械冲突照旧。
"""
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINE_FILE = ROOT / 'scripts/release/upstream-baseline'

VERSION_FILES = {'backend/cmd/server/VERSION', 'frontend/package.json'}
CONFLICT_BLOCK = re.compile(r'<<<<<<< [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n', re.S)


def git(*args, check=False):
    return subprocess.run(['git', *args], cwd=ROOT, text=True, capture_output=True, check=check)


def unmerged() -> list[str]:
    r = git('diff', '--name-only', '--diff-filter=U')
    return [p for p in r.stdout.splitlines() if p.strip()]


def stage_text(n: int, path: str) -> str | None:
    r = subprocess.run(['git', 'show', f':{n}:{path}'], cwd=ROOT, capture_output=True)
    if r.returncode != 0:
        return None
    try:
        return r.stdout.decode('utf-8')
    except UnicodeDecodeError:
        return None


def take_ours(path: str) -> bool:
    text = stage_text(2, path)
    if text is None:
        return False
    (ROOT / path).write_text(text, encoding='utf-8')
    git('add', '--', path)
    return True


def merge_changelog(path: str) -> bool:
    ours, theirs = stage_text(2, path), stage_text(3, path)
    if ours is None or theirs is None:
        return False

    def body(text: str) -> str:
        lines = [l.rstrip() for l in text.splitlines()]
        lines = [l for l in lines if l.strip() and not l.lstrip().startswith('#')]
        return '\n'.join(lines).strip()

    version = pathlib.Path(path).parent.name
    ours_body, theirs_body = body(ours), body(theirs)
    if ours_body == theirs_body:
        out = f'# {version}\n\n{ours_body}\n'
    else:
        out = (
            f'# {version}\n\n'
            f'## 本仓库（yiranhuo/ikun2api）\n\n{ours_body}\n\n'
            f'## 上游（1812095643/ikun2api）\n\n{theirs_body}\n'
        )
    (ROOT / path).write_text(out, encoding='utf-8')
    git('add', '--', path)
    return True


def sync_baseline_from_upstream() -> None:
    """把上游 release.yml 里的 upstream_baseline 抄进数据文件。"""
    r = subprocess.run(
        ['git', 'show', 'upstream-sync-ref:.github/workflows/release.yml'],
        cwd=ROOT, capture_output=True, text=True)
    if r.returncode != 0:
        return
    m = re.search(r"'upstream_baseline':'([^']*)'", r.stdout)
    if not m:
        return
    value = m.group(1).strip()
    current = BASELINE_FILE.read_text(encoding='utf-8').strip() if BASELINE_FILE.exists() else ''
    if value and value != current:
        BASELINE_FILE.write_text(value + '\n', encoding='utf-8')
        git('add', '--', 'scripts/release/upstream-baseline')
        print(f'上游基线 {current or "(空)"} -> {value}，已写入 scripts/release/upstream-baseline')


def main() -> int:
    # 冲突里只要出现 workflow，说明上游动了它。此时合并后不能推送带 workflow
    # 改动的提交，所以统一取本仓库版本，并把其中真正需要跟随的基线抽出来。
    paths = unmerged()
    if not paths:
        print('没有未解决的冲突')
        sync_baseline_from_upstream()
        return 0

    resolved, remaining = [], []
    for path in paths:
        ok, kind = False, ''
        if path in VERSION_FILES:
            ok, kind = take_ours(path), '取本地（合并后立即重算）'
        elif path.startswith('changelog/') and path.endswith('CHANGELOG.md'):
            ok, kind = merge_changelog(path), '本仓库 / 上游两节并存'
        elif path.startswith('.github/workflows/'):
            ok = take_ours(path)
            kind = '保留本仓库版本（GITHUB_TOKEN 不能推 workflow 改动）'
        else:
            kind = ''
        if ok:
            resolved.append((path, kind))
        else:
            remaining.append(path)

    for path, kind in resolved:
        print(f'已自动解决 {path} —— {kind}')
    if remaining:
        print('仍需人工处理的冲突：')
        for path in remaining:
            print(' -', path)
        return 1

    sync_baseline_from_upstream()
    print(f'全部 {len(resolved)} 个冲突已自动解决')
    return 0


if __name__ == '__main__':
    sys.exit(main())
