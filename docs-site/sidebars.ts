import type { SidebarsConfig } from '@docusaurus/plugin-content-docs'

const sidebars: SidebarsConfig = {
  corelibSidebar: [
    'intro',
    'getting-started',
    {
      type: 'category',
      label: 'Архитектура',
      collapsed: false,
      items: ['architecture/overview', 'architecture/design-decisions'],
    },
    {
      type: 'category',
      label: 'Пакеты',
      collapsed: false,
      items: [
        'packages/overview',
        'packages/ids',
        'packages/validate',
        'packages/errors',
        'packages/filter',
        'packages/db',
        'packages/operations',
        'packages/outbox',
        'packages/grpcsrv',
        'packages/grpcclient',
        'packages/authz',
        'packages/resilience',
        'packages/observability',
      ],
    },
  ],
}

export default sidebars
