// Colour data for all six palettes - mirrors the [data-palette="…"] blocks in
// style.css exactly. Needed as plain JS (not just CSS custom properties)
// because the palette picker's cards each render in the colours of the
// palette they represent, not the currently active one - a CSS variable only
// ever holds the *active* palette's value, so previewing "what would this
// look like if selected" needs the actual hex values here instead.
// name is an i18n key (see i18n.js's "palette_*" entries), not the literal
// display text - main.js's renderPaletteGrid runs it through t().
export const PALETTES = [
  {
    id: 'midnight', name: 'palette_midnight',
    bg: '#0E0B18', surface: '#171327', surfaceHigh: '#211C36', surfaceOutline: '#2E2748',
    primary: '#8B7CF6', primaryDeep: '#6D5AE0', accent: '#5B8DEF',
    textPrimary: '#F2EFFA', textSecondary: '#9C93B8', textMuted: '#6B6385',
  },
  {
    id: 'emerald', name: 'palette_emerald',
    bg: '#07140F', surface: '#0F2019', surfaceHigh: '#162C23', surfaceOutline: '#224034',
    primary: '#34D399', primaryDeep: '#10B981', accent: '#4ECDC4',
    textPrimary: '#ECFDF5', textSecondary: '#8CAFA1', textMuted: '#5E7D71',
  },
  {
    id: 'sunset', name: 'palette_sunset',
    bg: '#17090C', surface: '#261216', surfaceHigh: '#33191E', surfaceOutline: '#48252C',
    primary: '#FF7A59', primaryDeep: '#E85D3D', accent: '#FFB86C',
    textPrimary: '#FFF1EC', textSecondary: '#C0968D', textMuted: '#8C6A63',
  },
  {
    id: 'ocean', name: 'palette_ocean',
    bg: '#061320', surface: '#0C2033', surfaceHigh: '#122C45', surfaceOutline: '#1D3F5E',
    primary: '#38BDF8', primaryDeep: '#0EA5E9', accent: '#6EE7B7',
    textPrimary: '#ECFAFF', textSecondary: '#8AA9BF', textMuted: '#5C7A88',
  },
  {
    id: 'graphite', name: 'palette_graphite',
    bg: '#0D0D0F', surface: '#17171A', surfaceHigh: '#212126', surfaceOutline: '#2E2E35',
    primary: '#E4E4E7', primaryDeep: '#A1A1AA', accent: '#7DD3FC',
    textPrimary: '#F4F4F5', textSecondary: '#9A9AA5', textMuted: '#67676F',
  },
  {
    id: 'sakura', name: 'palette_sakura',
    bg: '#15090F', surface: '#23121B', surfaceHigh: '#301926', surfaceOutline: '#452537',
    primary: '#FF8FB1', primaryDeep: '#E05C8B', accent: '#C084FC',
    textPrimary: '#FFEFF5', textSecondary: '#C195A8', textMuted: '#8E6A79',
  },
];
