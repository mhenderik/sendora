// Dependency-free i18n helpers, safe to import from plain Node tests.
//   tr('English text')   marks a string that is only looked up dynamically (no-op at
//                        runtime; the key extractor finds it).
//   terr('English text') creates an Error whose message is translated at throw time.
// i18n/index.js registers the real translator; until then messages stay English.
let translator = s => s
export const tr = s => s
export const setTranslator = fn => { translator = typeof fn === 'function' ? fn : (s => s) }
export const translateMessage = s => translator(s)
export const terr = s => new Error(translator(s))
