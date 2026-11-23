import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import en from '../locales/en.json'

// Bundled, not fetched: a fixed-name locale URL can be served stale from browser cache.
i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: en } },
  interpolation: { escapeValue: false },
})

export default i18n
export { useTranslation, Trans } from 'react-i18next'
