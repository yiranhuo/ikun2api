#!/usr/bin/env python3
"""合并上游时自动解决「机械性」冲突，只把真正需要人看的冲突留给 issue。

上游与本仓库的分歧目前只有四类，全是版本管理衍生出来的，不需要判断力：

  1. backend/cmd/server/VERSION 与 frontend/package.json
     两边各自推进到不同补丁号。合并后 prepare-upstream-sync.py 会立刻按
     「取两边较大值再 +1」重算这两个文件，所以这里直接取本地值，冲突无意义。

  2. changelog/<版本>/CHANGELOG.md 的 add/add 冲突
     两边各自新建了同名目录、内容完全无关。git 无从合并，但两份内容都该保留，
     按「本仓库」与「上游」两节并存即可。

  3. .github/workflows/release.yml 里 release-manifest.json 那一行
     上游把仓库名写死成 1812095643/ikun2api，本仓库改成从 GITHUB_REPOSITORY
     派生。上游动这一行（比如把 upstream_baseline 从 0.2.24 推到 0.2.25）就必
     冲突。正确解法是取上游那一行——它带着上游真实进展的基线——只把写死的
     仓库名换回派生写法。

  4. 其它任何文件
     真冲突，交回给调用方（sync-upstream.yml 会 abort 并开 issue）。

退出码：0 = 全部解决，调用方 git commit 收尾即可；1 = 仍有真冲突，已打印清单。
"""
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]

DERIVED_REPO = "'repository':os.environ.get('GITHUB_REPOSITORY','yiranhuo/ikun2api')"

# 合并后立刻会被重算的文件，冲突时取本地值
VERSION_FILES = {'backend/cmd/server/VERSION', 'frontend/package.json'}

CONFLICT_BLOCK = re.compile(r'<<<<<<< [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n', re.S)


def git(*args, check=False):
    return subprocess.run(['git', *args], cwd=ROOT, text=True, capture_output=True, check=check)


def unmerged() -> list[str]:
    r = git('diff', '--name-only', '--diff-filter=U')
    return [p for p in r.stdout.splitlines() if p.strip()]


def stage(n: int, path: str) -> str | None:
    r = git('show', f':{n}:{path}')
    return r.stdout if r.returncode == 0 else None


def stage_text(n: int, path: str) -> str | None:
    """取冲突索引里的某一侧。二进制或缺失返回 None。"""
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
        # 去掉标题行与纯空行，只留条目正文
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


def resolve_release_workflow(path: str) -> bool:
    current = ROOT / path
    if not current.exists():
        return False
    text = current.read_text(encoding='utf-8')
    if '<<<<<<<' not in text:
        return False

    touched = []

    def fix(m: re.Match) -> str:
        upstream = m.group(2)
        touched.append(True)
        # 取上游那段，但把写死的仓库名换回派生写法
        return re.sub(r"'repository':'[^']*'", DERIVED_REPO, upstream)

    resolved = CONFLICT_BLOCK.sub(fix, text)
    if not touched:
        return False
    if '<<<<<<<' in resolved or '>>>>>>>' in resolved:
        # 还有别的冲突块没处理，交回人工
        return False
    current.write_text(resolved, encoding='utf-8')
    git('add', '--', path)
    return True


def main() -> int:
    paths = unmerged()
    if not paths:
        print('没有未解决的冲突')
        return 0

    resolved, remaining = [], []
    for path in paths:
        ok = False
        if path in VERSION_FILES:
            ok = take_ours(path)
            kind = '取本地（合并后立即重算）'
        elif path.startswith('changelog/') and path.endswith('CHANGELOG.md'):
            ok = merge_changelog(path)
            kind = '本仓库 / 上游两节并存'
        elif path == '.github/workflows/release.yml':
            ok = resolve_release_workflow(path)
            kind = '取上游基线 + 换回派生仓库名'
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
    print(f'全部 {len(resolved)} 个冲突已自动解决')
    return 0


if __name__ == '__main__':
    sys.exit(main())
