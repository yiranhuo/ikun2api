#!/usr/bin/env python3
"""合并后强制把 .github/workflows 恢复成本仓库版本，并把上游基线抽进数据文件。

原先我以为「冲突时取 --ours」就够了，但实际上游对 workflow 的改动有两种落法：
  - 有冲突 → 取 --ours 后该文件与 HEAD 一致，提交里没有差异，没问题；
  - 干净合并 → git 直接采纳上游版本，提交里就带上 workflow 差异 → push 被拒。

所以必须在提交前无条件把整个 .github/workflows 恢复成 HEAD 的样子（含删掉
上游新增的 workflow 文件），再从上游 release.yml 里抽出 upstream_baseline
写进 scripts/release/upstream-baseline —— 那才是唯一需要跟随上游的值。

这样无论上游怎么改 workflow，自动同步产出的提交都不含 workflow 改动。
"""
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINE_FILE = ROOT / 'scripts/release/upstream-baseline'


def git(*args, check=False):
    return subprocess.run(['git', *args], cwd=ROOT, text=True, capture_output=True, check=check)


def main() -> int:
    ref = (sys.argv[1] if len(sys.argv) > 1 else 'upstream/main').strip()

    # 1. 上游新增的 workflow 文件要删掉
    added = git('diff', '--name-only', '--diff-filter=A', 'HEAD', '--', '.github/workflows/')
    for path in [p for p in added.stdout.splitlines() if p.strip()]:
        git('rm', '-f', '--quiet', '--', path)
        print('移除上游新增的 workflow:', path)

    # 2. 其余 workflow 文件恢复成 HEAD 版本
    git('checkout', 'HEAD', '--', '.github/workflows/')
    git('add', '--', '.github/workflows/')
    print('已把 .github/workflows 恢复为本仓库版本')

    # 3. 抽取上游基线到普通数据文件
    r = subprocess.run(['git', 'show', f'{ref}:.github/workflows/release.yml'],
                       cwd=ROOT, capture_output=True, text=True)
    if r.returncode == 0:
        m = re.search(r"'upstream_baseline':'([^']*)'", r.stdout)
        if m:
            value = m.group(1).strip()
            current = BASELINE_FILE.read_text(encoding='utf-8').strip() if BASELINE_FILE.exists() else ''
            if value and value != current:
                BASELINE_FILE.write_text(value + '\n', encoding='utf-8')
                git('add', '--', 'scripts/release/upstream-baseline')
                print(f'上游基线 {current or "(空)"} -> {value}')
            else:
                print(f'上游基线未变：{value}')
        else:
            print('上游 release.yml 里没找到 upstream_baseline，跳过')
    else:
        print(f'读不到 {ref} 的 release.yml，跳过基线同步')

    # 4. 确认提交里确实没有 workflow 改动
    staged = git('diff', '--cached', '--name-only', 'HEAD', '--', '.github/workflows/')
    dirty = [p for p in staged.stdout.splitlines() if p.strip()]
    if dirty:
        print('::error::暂存区仍有 workflow 改动：' + ', '.join(dirty))
        return 1

    print('确认：暂存区没有 workflow 改动')
    return 0


if __name__ == '__main__':
    sys.exit(main())
