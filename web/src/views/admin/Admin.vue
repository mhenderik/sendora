<script setup>
import { t } from '../../i18n'
</script>
<template>
  <div>
    <div class="tabs">
      <router-link to="/admin/stats">{{ t('Statistics') }}</router-link>
      <router-link to="/admin/users">{{ t('Users') }}</router-link>
      <router-link to="/admin/access-review">{{ t('Access review') }}</router-link>
      <router-link to="/admin/settings">{{ t('Settings') }}</router-link>
      <router-link to="/admin/branding">{{ t('Branding') }}</router-link>
      <router-link to="/admin/themes">{{ t('Themes') }}</router-link>
      <router-link to="/admin/providers">{{ t('Login providers') }}</router-link>
      <router-link to="/admin/emails">{{ t('Email templates') }}</router-link>
      <router-link to="/admin/security">{{ t('Security') }}</router-link>
      <router-link to="/admin/languages">{{ t('Languages') }}</router-link>
      <router-link to="/admin/backups">{{ t('Backups') }}</router-link>
      <router-link to="/admin/logs">{{ t('Audit log') }}</router-link>
    </div>
    <router-view />
  </div>
</template>
