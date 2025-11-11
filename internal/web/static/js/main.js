/**
 * CPRA Monitoring Dashboard - Main JavaScript
 * Progressive enhancement with accessibility support
 */

(function() {
    'use strict';
    
    // ========================================================================
    // Theme Toggle
    // ========================================================================
    
    const initThemeToggle = () => {
        const themeToggle = document.getElementById('theme-toggle');
        if (!themeToggle) return;
        
        // Get saved theme preference or default to 'dark'
        const savedTheme = localStorage.getItem('cpra-theme') || 'dark';
        document.documentElement.setAttribute('data-theme', savedTheme);
        
        themeToggle.addEventListener('click', () => {
            const currentTheme = document.documentElement.getAttribute('data-theme') || 'dark';
            const newTheme = currentTheme === 'dark' ? 'light' : 'dark';
            
            document.documentElement.setAttribute('data-theme', newTheme);
            localStorage.setItem('cpra-theme', newTheme);
            
            // Announce theme change to screen readers
            announceToScreenReader(`Theme changed to ${newTheme} mode`);
        });
    };
    
    // ========================================================================
    // Accessibility Utilities
    // ========================================================================
    
    /**
     * Announce message to screen readers using aria-live region
     * @param {string} message - Message to announce
     * @param {string} priority - 'polite' or 'assertive'
     */
    const announceToScreenReader = (message, priority = 'polite') => {
        const announcement = document.createElement('div');
        announcement.className = 'sr-only';
        announcement.setAttribute('role', 'status');
        announcement.setAttribute('aria-live', priority);
        announcement.textContent = message;
        
        document.body.appendChild(announcement);
        
        // Remove after announcement
        setTimeout(() => announcement.remove(), 1000);
    };
    
    /**
     * Trap focus within a modal or dialog
     * @param {HTMLElement} element - Container element
     */
    const trapFocus = (element) => {
        const focusableElements = element.querySelectorAll(
            'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        
        if (focusableElements.length === 0) return;
        
        const firstElement = focusableElements[0];
        const lastElement = focusableElements[focusableElements.length - 1];
        
        element.addEventListener('keydown', (e) => {
            if (e.key !== 'Tab') return;
            
            if (e.shiftKey) {
                if (document.activeElement === firstElement) {
                    e.preventDefault();
                    lastElement.focus();
                }
            } else {
                if (document.activeElement === lastElement) {
                    e.preventDefault();
                    firstElement.focus();
                }
            }
        });
    };
    
    // ========================================================================
    // Keyboard Navigation
    // ========================================================================
    
    const initKeyboardNav = () => {
        // Escape key closes modals and menus
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                // Close any open modals
                document.querySelectorAll('[role="dialog"][aria-hidden="false"]').forEach(modal => {
                    closeModal(modal);
                });
                
                // Close any open dropdowns
                document.querySelectorAll('[aria-expanded="true"]').forEach(element => {
                    element.setAttribute('aria-expanded', 'false');
                });
            }
        });
    };
    
    // ========================================================================
    // Monitor Cards Interaction
    // ========================================================================
    
    const initMonitorCards = () => {
        // Handle monitor card options menu
        document.querySelectorAll('[data-menu]').forEach(button => {
            button.addEventListener('click', (e) => {
                e.stopPropagation();
                const monitorId = button.dataset.menu;
                toggleMonitorMenu(monitorId, button);
            });
        });
        
        // Close menus when clicking outside
        document.addEventListener('click', () => {
            document.querySelectorAll('.monitor-menu').forEach(menu => {
                menu.remove();
            });
        });
    };
    
    const toggleMonitorMenu = (monitorId, buttonElement) => {
        // Remove any existing menus
        document.querySelectorAll('.monitor-menu').forEach(menu => menu.remove());
        
        // Create menu
        const menu = document.createElement('div');
        menu.className = 'monitor-menu';
        menu.setAttribute('role', 'menu');
        menu.innerHTML = `
            <button role="menuitem" data-action="view" data-id="${monitorId}">
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M8 3.5a.5.5 0 01.5.5v4a.5.5 0 01-.5.5H4a.5.5 0 010-1h3.5V4a.5.5 0 01.5-.5z" fill="currentColor"/>
                </svg>
                View Details
            </button>
            <button role="menuitem" data-action="edit" data-id="${monitorId}">
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M11.013 1.427a1.75 1.75 0 012.474 0l1.086 1.086a1.75 1.75 0 010 2.474l-8.61 8.61c-.21.21-.47.364-.756.445l-3.251.93a.75.75 0 01-.927-.928l.929-3.25a1.75 1.75 0 01.445-.758l8.61-8.61z" fill="currentColor"/>
                </svg>
                Edit
            </button>
            <button role="menuitem" data-action="pause" data-id="${monitorId}">
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M5.5 3.5A1.5 1.5 0 014 5v6a1.5 1.5 0 003 0V5a1.5 1.5 0 00-1.5-1.5zm5 0A1.5 1.5 0 009 5v6a1.5 1.5 0 003 0V5a1.5 1.5 0 00-1.5-1.5z" fill="currentColor"/>
                </svg>
                Pause Monitoring
            </button>
            <hr class="monitor-menu__divider" />
            <button role="menuitem" data-action="delete" data-id="${monitorId}" class="monitor-menu__danger">
                <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M6.5 1.75a.25.25 0 01.25-.25h2.5a.25.25 0 01.25.25V3h-3V1.75zm4.5 0V3h2.25a.75.75 0 010 1.5H2.75a.75.75 0 010-1.5H5V1.75C5 .784 5.784 0 6.75 0h2.5C10.216 0 11 .784 11 1.75zM4.496 6.675a.75.75 0 10-1.492.15l.66 6.6A1.75 1.75 0 005.405 15h5.19c.9 0 1.652-.681 1.741-1.576l.66-6.6a.75.75 0 00-1.492-.149l-.66 6.6a.25.25 0 01-.249.225h-5.19a.25.25 0 01-.249-.225l-.66-6.6z" fill="currentColor"/>
                </svg>
                Delete
            </button>
        `;
        
        // Position menu
        const card = buttonElement.closest('.monitor-card');
        card.style.position = 'relative';
        card.appendChild(menu);
        
        // Handle menu actions
        menu.querySelectorAll('[data-action]').forEach(item => {
            item.addEventListener('click', (e) => {
                e.stopPropagation();
                const action = item.dataset.action;
                const id = item.dataset.id;
                handleMonitorAction(action, id);
                menu.remove();
            });
        });
        
        // Focus first menu item
        menu.querySelector('[role="menuitem"]').focus();
        
        // Trap focus in menu
        trapFocus(menu);
    };
    
    const handleMonitorAction = async (action, monitorId) => {
        switch(action) {
            case 'view':
                window.location.href = `/monitors/${monitorId}`;
                break;
            case 'edit':
                window.location.href = `/monitors/${monitorId}/edit`;
                break;
            case 'pause':
                await toggleMonitorPause(monitorId);
                break;
            case 'delete':
                if (confirm('Are you sure you want to delete this monitor?')) {
                    await deleteMonitor(monitorId);
                }
                break;
        }
    };
    
    const toggleMonitorPause = async (monitorId) => {
        try {
            const response = await fetch(`/api/monitors/${monitorId}/pause`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                }
            });
            
            if (response.ok) {
                announceToScreenReader('Monitor paused successfully');
                // Reload or update UI
                window.location.reload();
            } else {
                throw new Error('Failed to pause monitor');
            }
        } catch (error) {
            console.error('Error pausing monitor:', error);
            announceToScreenReader('Error: Could not pause monitor', 'assertive');
        }
    };
    
    const deleteMonitor = async (monitorId) => {
        try {
            const response = await fetch(`/api/monitors/${monitorId}`, {
                method: 'DELETE'
            });
            
            if (response.ok) {
                announceToScreenReader('Monitor deleted successfully');
                // Remove card from DOM
                const card = document.querySelector(`[data-id="${monitorId}"]`);
                if (card) {
                    card.style.opacity = '0';
                    setTimeout(() => card.remove(), 300);
                }
            } else {
                throw new Error('Failed to delete monitor');
            }
        } catch (error) {
            console.error('Error deleting monitor:', error);
            announceToScreenReader('Error: Could not delete monitor', 'assertive');
        }
    };
    
    // ========================================================================
    // Alert Acknowledgement
    // ========================================================================
    
    const initAlertAcknowledgement = () => {
        document.querySelectorAll('[data-ack]').forEach(button => {
            button.addEventListener('click', async (e) => {
                const alertId = button.dataset.ack;
                await acknowledgeAlert(alertId, button);
            });
        });
    };
    
    const acknowledgeAlert = async (alertId, buttonElement) => {
        try {
            const response = await fetch(`/api/alerts/${alertId}/acknowledge`, {
                method: 'POST'
            });
            
            if (response.ok) {
                buttonElement.disabled = true;
                buttonElement.textContent = 'Acknowledged';
                announceToScreenReader('Alert acknowledged');
                
                // Fade out the alert item after 1 second
                setTimeout(() => {
                    const alertItem = buttonElement.closest('.alert-item');
                    if (alertItem) {
                        alertItem.style.opacity = '0';
                        setTimeout(() => alertItem.remove(), 300);
                    }
                }, 1000);
            } else {
                throw new Error('Failed to acknowledge alert');
            }
        } catch (error) {
            console.error('Error acknowledging alert:', error);
            announceToScreenReader('Error: Could not acknowledge alert', 'assertive');
        }
    };
    
    // ========================================================================
    // Performance Optimizations
    // ========================================================================
    
    /**
     * Debounce function to limit rate of function calls
     * @param {Function} func - Function to debounce
     * @param {number} wait - Wait time in milliseconds
     * @returns {Function} Debounced function
     */
    const debounce = (func, wait) => {
        let timeout;
        return function executedFunction(...args) {
            const later = () => {
                clearTimeout(timeout);
                func(...args);
            };
            clearTimeout(timeout);
            timeout = setTimeout(later, wait);
        };
    };
    
    /**
     * Lazy load images with Intersection Observer
     */
    const initLazyLoading = () => {
        if ('IntersectionObserver' in window) {
            const imageObserver = new IntersectionObserver((entries, observer) => {
                entries.forEach(entry => {
                    if (entry.isIntersecting) {
                        const img = entry.target;
                        img.src = img.dataset.src;
                        img.classList.remove('lazy');
                        observer.unobserve(img);
                    }
                });
            });
            
            document.querySelectorAll('img.lazy').forEach(img => imageObserver.observe(img));
        }
    };
    
    // ========================================================================
    // Form Validation
    // ========================================================================
    
    const initFormValidation = () => {
        document.querySelectorAll('form[data-validate]').forEach(form => {
            form.addEventListener('submit', (e) => {
                if (!validateForm(form)) {
                    e.preventDefault();
                }
            });
            
            // Real-time validation on blur
            form.querySelectorAll('input, textarea, select').forEach(field => {
                field.addEventListener('blur', () => validateField(field));
            });
        });
    };
    
    const validateForm = (form) => {
        let isValid = true;
        const fields = form.querySelectorAll('input, textarea, select');
        
        fields.forEach(field => {
            if (!validateField(field)) {
                isValid = false;
            }
        });
        
        if (!isValid) {
            announceToScreenReader('Form contains errors. Please correct them and try again.', 'assertive');
        }
        
        return isValid;
    };
    
    const validateField = (field) => {
        const errorElement = field.parentElement.querySelector('.field-error');
        let isValid = true;
        let errorMessage = '';
        
        // Required field validation
        if (field.hasAttribute('required') && !field.value.trim()) {
            isValid = false;
            errorMessage = 'This field is required';
        }
        
        // Email validation
        if (field.type === 'email' && field.value && !isValidEmail(field.value)) {
            isValid = false;
            errorMessage = 'Please enter a valid email address';
        }
        
        // URL validation
        if (field.type === 'url' && field.value && !isValidURL(field.value)) {
            isValid = false;
            errorMessage = 'Please enter a valid URL';
        }
        
        // Update UI
        if (isValid) {
            field.classList.remove('field--error');
            field.setAttribute('aria-invalid', 'false');
            if (errorElement) errorElement.textContent = '';
        } else {
            field.classList.add('field--error');
            field.setAttribute('aria-invalid', 'true');
            if (errorElement) {
                errorElement.textContent = errorMessage;
                field.setAttribute('aria-describedby', errorElement.id);
            }
        }
        
        return isValid;
    };
    
    const isValidEmail = (email) => {
        return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
    };
    
    const isValidURL = (url) => {
        try {
            new URL(url);
            return true;
        } catch {
            return false;
        }
    };
    
    // ========================================================================
    // Initialization
    // ========================================================================
    
    const init = () => {
        // Core functionality
        initThemeToggle();
        initKeyboardNav();
        initMonitorCards();
        initAlertAcknowledgement();
        initLazyLoading();
        initFormValidation();
        
        // Announce page loaded to screen readers
        announceToScreenReader('Dashboard loaded');
        
        // Add loaded class for CSS transitions
        document.body.classList.add('loaded');
    };
    
    // Initialize when DOM is ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
    
    // ========================================================================
    // Public API (if needed by other scripts)
    // ========================================================================
    
    window.CPRA = {
        announceToScreenReader,
        trapFocus,
        debounce
    };
    
})();
