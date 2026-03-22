// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
/**
 * Spiral Pool Dashboard - Theme Manager
 * Handles loading, applying, and switching between themes.
 * Themes are JSON-based and fully customizable.
 *
 * See LICENSE file for full BSD-3-Clause license terms.
 */

class ThemeManager {
    constructor() {
        this.currentTheme = null;
        this.themes = {};
        this.defaultTheme = 'cyberpunk';
        this.storageKey = 'spiralpool_theme';
        this.themeStyleElement = null;
    }

    /**
     * Initialize the theme manager
     */
    async init() {
        // Create style element for theme CSS
        this.themeStyleElement = document.createElement('style');
        this.themeStyleElement.id = 'theme-dynamic-styles';
        document.head.appendChild(this.themeStyleElement);

        // Load available themes
        await this.loadThemes();

        // Load saved theme or use default
        const savedTheme = localStorage.getItem(this.storageKey) || this.defaultTheme;
        await this.applyTheme(savedTheme);

        // Setup theme selector if present
        this.setupThemeSelector();
    }

    /**
     * Load all available themes from the themes directory
     */
    async loadThemes() {
        const themeFiles = [
            // Core themes
            'cyberpunk',        // Default - neon futuristic
            'black-ice',        // Sleek dark with ice blue accents
            '1337-h4x0r',       // Hacker terminal green
            // Popular editor themes
            'dracula',          // Classic Dracula palette
            'nord',             // Arctic north-bluish
            'tokyo-night',      // Tokyo cityscape at night
            'gruvbox-dark',     // Retro groove colors
            // Nature & atmosphere
            'midnight-aurora',  // Dark sky with northern lights
            'solar-flare',      // Warm orange/yellow sun energy
            'ocean-depths',     // Deep blue underwater
            'wood-paneling',    // Warm rustic wood tones
            // Seasonal
            'spring-bloom',     // Fresh spring colors
            'summer-vibes',     // Warm tropical summer
            'autumn-harvest',   // Rich fall foliage
            'winter-frost',     // Cool icy winter
            // Fun
            'rainbow-unicorn'   // Absurd rainbow sparkle
        ];

        for (const themeName of themeFiles) {
            try {
                const response = await fetch(`/static/themes/${themeName}.json`);
                if (response.ok) {
                    const theme = await response.json();
                    this.themes[themeName] = theme;
                }
            } catch (e) {
                console.warn(`Failed to load theme: ${themeName}`, e);
            }
        }

        // Ensure we have at least the default theme inline
        if (!this.themes[this.defaultTheme]) {
            this.themes[this.defaultTheme] = this.getDefaultThemeObject();
        }
    }

    /**
     * Get the default theme object (fallback)
     */
    getDefaultThemeObject() {
        return {
            "name": "Cyberpunk",
            "id": "cyberpunk",
            "author": "Spiral Pool",
            "version": "1.0.0",
            "description": "Neon-infused futuristic mining dashboard",
            "colors": {
                "bg-primary": "#0f1419",
                "bg-secondary": "#1a1f2e",
                "bg-card": "#1e2433",
                "bg-card-hover": "#252d3d",
                "neon-blue": "#3b9dff",
                "neon-cyan": "#22d3ee",
                "neon-purple": "#a78bfa",
                "neon-pink": "#f472b6",
                "neon-orange": "#fb923c",
                "neon-yellow": "#facc15",
                "neon-green": "#4ade80",
                "neon-red": "#f87171",
                "text-primary": "#f1f5f9",
                "text-secondary": "#a1a9b8",
                "text-muted": "#6b7280",
                "status-online": "#4ade80",
                "status-offline": "#f87171",
                "status-warning": "#facc15"
            },
            "gradients": {
                "primary": "linear-gradient(135deg, #3b9dff 0%, #0284c7 100%)",
                "secondary": "linear-gradient(135deg, #a78bfa 0%, #7c3aed 100%)",
                "accent": "linear-gradient(135deg, #fb923c 0%, #f97316 100%)"
            },
            "fonts": {
                "display": "'Orbitron', sans-serif",
                "body": "'Rajdhani', sans-serif",
                "mono": "'Share Tech Mono', monospace"
            },
            "effects": {
                "glowIntensity": 0.2,
                "animationSpeed": 1,
                "borderRadius": "12px",
                "backgroundStyle": "grid",
                "useGlitchEffect": true,
                "useParticles": true,
                "useScanLines": true
            }
        };
    }

    /**
     * Apply a theme by name
     */
    async applyTheme(themeName) {
        const theme = this.themes[themeName];
        if (!theme) {
            console.warn(`Theme not found: ${themeName}, using default`);
            return this.applyTheme(this.defaultTheme);
        }

        this.currentTheme = theme;
        localStorage.setItem(this.storageKey, themeName);

        // Generate and apply CSS variables
        const css = this.generateThemeCSS(theme);
        this.themeStyleElement.textContent = css;

        // Update body class for theme-specific styles
        document.body.className = document.body.className
            .replace(/theme-\S+/g, '')
            .trim() + ` theme-${themeName}`;

        // Apply special effects based on theme settings
        this.applyEffects(theme);

        // Dispatch event for other components
        window.dispatchEvent(new CustomEvent('themeChanged', { detail: theme }));

        return theme;
    }

    /**
     * Generate CSS from theme object
     */
    generateThemeCSS(theme) {
        let css = ':root {\n';

        // Colors
        for (const [key, value] of Object.entries(theme.colors || {})) {
            css += `  --${key}: ${value};\n`;
        }

        // Gradients
        for (const [key, value] of Object.entries(theme.gradients || {})) {
            css += `  --gradient-${key}: ${value};\n`;
        }

        // Fonts
        for (const [key, value] of Object.entries(theme.fonts || {})) {
            css += `  --font-${key}: ${value};\n`;
        }

        // Effects as CSS variables
        if (theme.effects) {
            css += `  --glow-intensity: ${theme.effects.glowIntensity || 0.2};\n`;
            css += `  --animation-speed: ${theme.effects.animationSpeed || 1};\n`;
            css += `  --border-radius: ${theme.effects.borderRadius || '12px'};\n`;
        }

        css += '}\n\n';

        // Add shadow/glow utilities
        if (theme.colors) {
            css += `
/* Dynamic Glow Effects */
.glow-primary { box-shadow: 0 0 calc(15px * var(--glow-intensity)) ${theme.colors['neon-blue'] || '#3b9dff'}; }
.glow-secondary { box-shadow: 0 0 calc(15px * var(--glow-intensity)) ${theme.colors['neon-purple'] || '#a78bfa'}; }
.glow-success { box-shadow: 0 0 calc(15px * var(--glow-intensity)) ${theme.colors['neon-green'] || '#4ade80'}; }
.glow-warning { box-shadow: 0 0 calc(15px * var(--glow-intensity)) ${theme.colors['neon-orange'] || '#fb923c'}; }
.glow-danger { box-shadow: 0 0 calc(15px * var(--glow-intensity)) ${theme.colors['neon-red'] || '#f87171'}; }
`;
        }

        // Add theme-specific overrides (sanitized)
        if (theme.customCSS) {
            // Strip patterns that could exfiltrate data or load external resources
            let sanitized = theme.customCSS
                .replace(/@import\b/gi, '/* blocked @import */')
                .replace(/url\s*\(/gi, '/* blocked url( */')
                .replace(/expression\s*\(/gi, '/* blocked expression( */')
                .replace(/behavior\s*:/gi, '/* blocked behavior: */')
                .replace(/-moz-binding\s*:/gi, '/* blocked -moz-binding: */')
                .replace(/javascript\s*:/gi, '/* blocked javascript: */');
            css += '\n/* Theme Custom CSS */\n';
            css += sanitized;
        }

        return css;
    }

    /**
     * Apply special visual effects based on theme settings
     */
    applyEffects(theme) {
        const effects = theme.effects || {};

        // Apply background style
        const grid = document.querySelector('.cyber-grid');
        if (grid) {
            grid.setAttribute('data-bg-style', effects.backgroundStyle || 'grid');
        }

        // Toggle glitch effect
        const glitchElements = document.querySelectorAll('.glitch');
        glitchElements.forEach(el => {
            el.style.animationPlayState = effects.useGlitchEffect ? 'running' : 'paused';
        });

        // Toggle particles
        const particles = document.querySelector('.cyber-particles');
        if (particles) {
            particles.style.display = effects.useParticles ? 'block' : 'none';
        }

        // Toggle scan lines
        const scanLines = document.querySelector('.scan-line');
        if (scanLines) {
            scanLines.style.display = effects.useScanLines ? 'block' : 'none';
        }

        // Set animation speed
        document.documentElement.style.setProperty('--animation-multiplier', effects.animationSpeed || 1);
    }

    /**
     * Setup the theme selector dropdown
     */
    setupThemeSelector() {
        const selector = document.getElementById('theme-selector');
        if (!selector) return;

        // Clear existing options
        selector.innerHTML = '';

        // Add themes to selector
        for (const [id, theme] of Object.entries(this.themes)) {
            const option = document.createElement('option');
            option.value = id;
            option.textContent = theme.name || id;
            if (this.currentTheme && this.currentTheme.id === id) {
                option.selected = true;
            }
            selector.appendChild(option);
        }

        // Handle theme change
        selector.addEventListener('change', async (e) => {
            await this.applyTheme(e.target.value);
        });
    }

    /**
     * Get current theme object
     */
    getCurrentTheme() {
        return this.currentTheme;
    }

    /**
     * Get list of available themes
     */
    getAvailableThemes() {
        return Object.entries(this.themes).map(([id, theme]) => ({
            id,
            name: theme.name,
            description: theme.description,
            author: theme.author
        }));
    }

    /**
     * Export current theme as JSON
     */
    exportTheme() {
        return JSON.stringify(this.currentTheme, null, 2);
    }

    /**
     * Import a custom theme from JSON
     */
    importTheme(jsonString) {
        try {
            const theme = JSON.parse(jsonString);
            if (!theme.id || !theme.name) {
                throw new Error('Theme must have id and name properties');
            }
            if (!/^[a-zA-Z0-9_-]+$/.test(theme.id) || theme.id.length > 64) {
                throw new Error('Invalid theme ID format');
            }
            this.themes[theme.id] = theme;
            return theme;
        } catch (e) {
            console.error('Failed to import theme:', e);
            throw e;
        }
    }
}

// Global theme manager instance
window.themeManager = new ThemeManager();

// Initialize on DOM ready
document.addEventListener('DOMContentLoaded', () => {
    window.themeManager.init();
});
