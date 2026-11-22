#!/usr/bin/env python3
"""Bump the release version after an upstream sync so it never goes backwards.

上游 `1812095643/ikun2api` 的版本号可能低于本仓库（本仓库在其之上发布了
发布源切换、受监护升级自动重启等改动）。直接沿用合并进来的 VERSION 会让
线上更新器判定「没有更新」，所以这里取合并前后两者的较大值，再进一位
patch，保证版本号只升不降。
"""
import argparse
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[2]
SEMVER = re.compile(r'\d+\.\d+\.\d+')


def parse(text, label):
    value = text.strip()
    if not SEMVER.fullmatch(value):
        raise SystemExit(f'{label} 不是合法的 x.y.z 版本号：{value!r}')
    return tuple(int(part) for part in value.split('.'))


def main():
    parser = argparse.ArgumentParser(description='同步上游后计算并写入新版本号')
    parser.add_argument('--local-version', required=True,
                        help='合并前本仓库的版本号（backend/cmd/server/VERSION 的旧值）')
    parser.add_argument('--note', default='自动同步上游 1812095643/ikun2api')
    args = parser.parse_args()

    local = parse(args.local_version, '合并前版本')
    version_file = ROOT / 'backend/cmd/server/VERSION'
    merged = parse(version_file.read_text(), '合并后版本')

    base = max(local, merged)
    version = '%d.%d.%d' % (base[0], base[1], base[2] + 1)

    version_file.write_text(version + '\n')

    package_path = ROOT / 'frontend/package.json'
    package = package_path.read_text()
    package, replaced = re.subn(
        r'("version"\s*:\s*")[^"]+(")',
        lambda m: m.group(1) + version + m.group(2),
        package,
        count=1,
    )
    if replaced != 1:
        raise SystemExit('frontend/package.json 里找不到 "version" 字段')
    package_path.write_text(package)

    changelog = ROOT / 'changelog' / version
    changelog.mkdir(parents=True, exist_ok=True)
    (changelog / 'CHANGELOG.md').write_text(
        f'# IKUN2api {version}\n\n'
        f'## 变更\n\n'
        f'- {args.note}。\n'
        f'- 合并前本仓库版本 {args.local_version}，上游 VERSION 为 '
        f'{".".join(str(x) for x in merged)}，取较大值后进一位 patch。\n'
        f'- 具体改动见本次合并提交与上游 release notes。\n'
    )

    print(version)


if __name__ == '__main__':
    main()
