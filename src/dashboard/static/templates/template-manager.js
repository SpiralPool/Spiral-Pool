// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
/**
 * Spiral Pool Dashboard - Template Manager
 * Handles loading, applying, and switching between dashboard templates.
 * Templates define the layout and which components are visible.
 *
 * See LICENSE file for full BSD-3-Clause license terms.
 */

class TemplateManager {
    constructor() {
        this.currentTemplate = null;
        this.templates = Object.create(null);
        this.defaultTemplate = 'spiral-pool-default';
        this.storageKey = 'spiralpool_template';
    }

    /**
     * Initialize the template manager
     */
    async init() {
        // Load available templates
        await this.loadTemplates();

        // Load saved template or use default
        const savedTemplate = localStorage.getItem(this.storageKey) || this.defaultTemplate;
        const safeName = /^[a-zA-Z0-9_-]+$/.test(savedTemplate) ? savedTemplate : this.defaultTemplate;
        await this.applyTemplate(safeName);

        // Setup template selector if present
        this.setupTemplateSelector();
    }

    /**
     * Load all available templates
     */
    async loadTemplates() {
        const templateFiles = [
            'spiral-pool-default',  // Full-featured default
            'mining-ops-center',    // Operations focused
            'lottery-lucky',        // Lottery/luck focused
            'power-efficiency',     // Power/efficiency focused
            'compact-minimal'       // Minimal compact view
        ];

        for (const templateName of templateFiles) {
            try {
                const response = await fetch(`/static/templates/${templateName}.json`);
                if (response.ok) {
                    const template = await response.json();
                    this.templates[templateName] = template;
                }
            } catch (e) {
                console.warn(`Failed to load template: ${templateName}`, e);
            }
        }

        // Ensure we have at least the default template
        if (!this.templates[this.defaultTemplate]) {
            this.templates[this.defaultTemplate] = this.getDefaultTemplateObject();
        }
    }

    /**
     * Get the default template object (fallback)
     */
    getDefaultTemplateObject() {
        return {
            "name": "Spiral Pool Default",
            "id": "spiral-pool-default",
            "author": "Spiral Pool",
            "version": "2.0.1",
            "description": "Complete dashboard with all mining statistics and features",
            "layout": {
                "statsRow": true,
                "chartsRow": true,
                "lifetimeSection": true,
                "healthSection": true,
                "minersSection": true
            },
            "components": {
                "stats": {
                    "hashrate": true,
                    "power": true,
                    "shares": true,
                    "blocks": true,
                    "difficulty": true,
                    "miners": true
                },
                "charts": {
                    "hashrateHistory": true,
                    "earningsCalculator": true
                },
                "lifetime": {
                    "totalShares": true,
                    "blocksFound": true,
                    "bestShare": true,
                    "uptime": true
                },
                "health": {
                    "pool": true,
                    "node": true
                },
                "miners": {
                    "showAll": true,
                    "showActions": true,
                    "showTemperature": true,
                    "showPower": true,
                    "compactMode": false
                }
            },
            "customization": {
                "gridColumns": "auto",
                "statsPerRow": 6,
                "minersPerRow": 3,
                "showFooterSlogan": true
            }
        };
    }

    /**
     * Apply a template by name
     */
    async applyTemplate(templateName) {
        const template = this.templates[templateName];
        if (!template) {
            console.warn(`Template not found: ${templateName}, using default`);
            return this.applyTemplate(this.defaultTemplate);
        }

        this.currentTemplate = template;
        localStorage.setItem(this.storageKey, templateName);

        // Apply layout visibility
        this.applyLayout(template.layout);

        // Apply component settings
        this.applyComponents(template.components);

        // Apply customization
        this.applyCustomization(template.customization);

        // Update body class
        document.body.className = document.body.className
            .replace(/template-\S+/g, '')
            .trim() + ` template-${templateName}`;

        // Dispatch event
        window.dispatchEvent(new CustomEvent('templateChanged', { detail: template }));

        return template;
    }

    /**
     * Apply layout visibility settings
     */
    applyLayout(layout) {
        if (!layout) return;

        const sections = {
            'statsRow': '.stats-overview',
            'chartsRow': '.charts-row',
            'lifetimeSection': '.lifetime-section',
            'healthSection': '.health-section',
            'minersSection': '.miners-section'
        };

        for (const [key, selector] of Object.entries(sections)) {
            const element = document.querySelector(selector);
            if (element) {
                element.style.display = layout[key] !== false ? '' : 'none';
            }
        }
    }

    /**
     * Apply component-specific settings
     */
    applyComponents(components) {
        if (!components) return;

        // Stats visibility
        if (components.stats) {
            const statCards = document.querySelectorAll('.stat-card');
            const statTypes = ['hashrate', 'power', 'shares', 'blocks', 'difficulty', 'temp'];
            statCards.forEach((card, index) => {
                const type = statTypes[index];
                if (type && components.stats[type] === false) {
                    card.style.display = 'none';
                } else {
                    card.style.display = '';
                }
            });
        }

        // Charts visibility
        if (components.charts) {
            const chartCard = document.querySelector('.chart-card');
            const earningsCard = document.querySelector('.earnings-card');
            if (chartCard) chartCard.style.display = components.charts.hashrateHistory !== false ? '' : 'none';
            if (earningsCard) earningsCard.style.display = components.charts.earningsCalculator !== false ? '' : 'none';
        }

        // Lifetime stats visibility
        if (components.lifetime) {
            const lifetimeCards = document.querySelectorAll('.lifetime-card');
            const lifetimeTypes = ['totalShares', 'blocksFound', 'bestShare', 'uptime'];
            lifetimeCards.forEach((card, index) => {
                const type = lifetimeTypes[index];
                if (type && components.lifetime[type] === false) {
                    card.style.display = 'none';
                } else {
                    card.style.display = '';
                }
            });
        }

        // Health section
        if (components.health) {
            const poolHealth = document.querySelector('.pool-health');
            const nodeHealth = document.querySelector('.node-health');
            if (poolHealth) poolHealth.style.display = components.health.pool !== false ? '' : 'none';
            if (nodeHealth) nodeHealth.style.display = components.health.node !== false ? '' : 'none';
        }

        // Miner cards
        if (components.miners) {
            const minersGrid = document.querySelector('.miners-grid');
            if (minersGrid) {
                if (components.miners.compactMode) {
                    minersGrid.classList.add('compact-mode');
                } else {
                    minersGrid.classList.remove('compact-mode');
                }
            }
        }
    }

    /**
     * Apply customization settings
     */
    applyCustomization(customization) {
        if (!customization) return;

        const root = document.documentElement;

        // Grid columns
        if (customization.statsPerRow) {
            const spr = parseInt(customization.statsPerRow, 10);
            if (spr > 0 && spr <= 12) {
                root.style.setProperty('--stats-per-row', spr);
            }
        }

        if (customization.minersPerRow) {
            const mpr = parseInt(customization.minersPerRow, 10);
            if (mpr > 0 && mpr <= 12) {
                root.style.setProperty('--miners-per-row', mpr);
            }
        }

        // Footer slogan
        const footerSlogan = document.querySelector('.footer-slogan');
        if (footerSlogan) {
            footerSlogan.style.display = customization.showFooterSlogan !== false ? '' : 'none';
        }
    }

    /**
     * Setup the template selector dropdown
     */
    setupTemplateSelector() {
        const selector = document.getElementById('template-selector');
        if (!selector) return;

        // Clear existing options
        selector.innerHTML = '';

        // Add templates to selector
        for (const [id, template] of Object.entries(this.templates)) {
            const option = document.createElement('option');
            option.value = id;
            option.textContent = template.name || id;
            if (this.currentTemplate && this.currentTemplate.id === id) {
                option.selected = true;
            }
            selector.appendChild(option);
        }

        // Handle template change
        selector.addEventListener('change', async (e) => {
            await this.applyTemplate(e.target.value);
            // Refresh data to update miner cards with new settings
            if (typeof refreshData === 'function') {
                refreshData();
            }
        });
    }

    /**
     * Get current template object
     */
    getCurrentTemplate() {
        return this.currentTemplate;
    }

    /**
     * Get list of available templates
     */
    getAvailableTemplates() {
        return Object.entries(this.templates).map(([id, template]) => ({
            id,
            name: template.name,
            description: template.description,
            author: template.author
        }));
    }

    /**
     * Export current template as JSON
     */
    exportTemplate() {
        return JSON.stringify(this.currentTemplate, null, 2);
    }

    /**
     * Import a custom template from JSON
     */
    importTemplate(jsonString) {
        try {
            const template = JSON.parse(jsonString);
            if (!template.id || !template.name) {
                throw new Error('Template must have id and name properties');
            }
            // SECURITY: Validate template ID (prevents CSS class injection + prototype pollution)
            if (!/^[a-zA-Z0-9_-]+$/.test(template.id) || template.id.length > 64) {
                throw new Error('Template id must contain only alphanumeric characters, hyphens, and underscores (max 64 chars)');
            }
            this.templates[template.id] = template;
            return template;
        } catch (e) {
            console.error('Failed to import template:', e);
            throw e;
        }
    }
}

// Global template manager instance
window.templateManager = new TemplateManager();

// Initialize on DOM ready
document.addEventListener('DOMContentLoaded', () => {
    window.templateManager.init();
});
