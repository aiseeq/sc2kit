# ТЗ sc2kit как сделано + критик

Статус: в работе
Исполнитель: git-admin

## Что сделать
- Задание лида (31.10 ~15:4x CET) через git-admin (тикет ~/git/docs/tasks/20261031-1532-MEDIUM-reverse-spec-rollout.md): ТЗ sc2kit «как сделано» + критика. Общий бриф /home/aisee/docs/reverse-spec/brief.md. Репо публичное: ТЗ (с цитатами и tl id) — в /home/aisee/.claude/tools/sc2kit/spec.md (claude-config), не в репо; ветка spec-as-built не вливается.
- Автор: боец Terra (класс документация), бриф /home/aisee/.cache/worktrees/reverse-spec/sc2kit/brief.md, пишет docs/spec.md.
- Критик: свежий боец Terra (класс ревью; Opus — только если Terra не справилась), вход — docs/spec.md, код и все источники, без вывода автора.
- Разбор: принято → tk на доработку, отклонено → причина строкой; разбор проверяет свежий агент.

## Приёмка
- Автор: /home/aisee/.cache/worktrees/reverse-spec/accept.sh 60 client.go game.go errors.go api/ proto/ examples/ping Makefile в корне дерева — код 0; на дереве без docs/spec.md — код 1.
- ТЗ в /home/aisee/.claude/tools/sc2kit/spec.md, закоммичено в claude-config; файл находок критика; у каждой находки решение (тикет или причина); итог git-admin.

## Журнал
- 2026-10-31 15:38 открыт — git-admin
- 2026-10-31 15:38 в работе — git-admin
- 2026-10-31 15:39 note — git-admin: автор: worker 1031-1539-spec-as-built-5 (terra, документация, --max 2h, --escalate none) запущен 15:39 CET; дерево ; бриф и приёмка — /home/aisee/.cache/worktrees/reverse-spec/sc2kit/{brief.md,accept.cmd}; на дереве без spec приёмка — код 1
- 2026-10-31 15:39 note — git-admin: дерево автора: /home/aisee/.cache/worktrees/sc2kit/spec-as-built (в прошлой строке путь пуст)
