import Clutter from 'gi://Clutter';
import GObject from 'gi://GObject';
import Pango from 'gi://Pango';
import St from 'gi://St';

import {meterDisplay} from './state.js';

// Include rounded -90 as well as the distinct silence and unavailable labels.
const levels = [null, ...Array.from({length: 91}, (_, i) => -i), -89.9];
const levelTexts = levels.map(peak => meterDisplay(peak === null ? null : {
    available: true, peak_dbfs: peak, rms_dbfs: peak, clipping: false,
}).text);

export const IconBox = GObject.registerClass(
class IconBox extends St.Widget {
    _init(icon, drawing) {
        super._init();
        this._icon = icon;
        this._drawing = drawing;
        this.add_child(icon);
        this.add_child(drawing);
        icon.connectObject('style-changed', () => this.queue_relayout(), this);
    }

    _iconSize() {
        const [found, size] = this._icon.get_theme_node().lookup_length('icon-size', false);
        const scale = St.ThemeContext.get_for_stage(global.stage).scale_factor;
        // Match St.Icon's default, without depending on asynchronously loaded textures.
        if (this._icon.icon_size > 0)
            return this._icon.icon_size * scale;
        return found && size > 0 ? Math.round(size) : 16 * scale;
    }

    vfunc_get_preferred_width(_forHeight) {
        const size = this._iconSize();
        const widths = this._icon.get_theme_node().adjust_preferred_width(size, size);
        // Clutter subtracts actor margins during allocate(); theme-node sizing excludes them.
        const margin = this._icon.margin_left + this._icon.margin_right;
        return widths.map(width => width + margin);
    }

    vfunc_get_preferred_height(_forWidth) {
        const size = this._iconSize();
        const heights = this._icon.get_theme_node().adjust_preferred_height(size, size);
        const margin = this._icon.margin_top + this._icon.margin_bottom;
        return heights.map(height => height + margin);
    }

    vfunc_allocate(box) {
        this.set_allocation(box);
        const local = new Clutter.ActorBox({
            x1: 0, y1: 0, x2: box.get_width(), y2: box.get_height(),
        });
        this._icon.allocate(local);
        const iconBox = this._icon.get_allocation_box();
        const content = this._icon.get_theme_node().get_content_box(iconBox);
        content.set_origin(content.x1 + iconBox.x1, content.y1 + iconBox.y1);
        this._drawing.allocate(content);
    }
});

export const LevelLabel = GObject.registerClass(
class LevelLabel extends St.Label {
    _init(params = {}, prefix = '') {
        super._init(params);
        this._texts = levelTexts.map(text => `${prefix}${text}`);
        this._measuredWidth = null;
        this.clutter_text.ellipsize = Pango.EllipsizeMode.NONE;
        this.clutter_text.connectObject(
            'notify::font-description', () => this._invalidateWidth(),
            'notify::attributes', () => this._invalidateWidth(), this);
    }

    _invalidateWidth() {
        this._measuredWidth = null;
        this.queue_relayout();
    }

    vfunc_style_changed() {
        super.vfunc_style_changed();
        this._measuredWidth = null;
    }

    vfunc_get_preferred_width(_forHeight) {
        const node = this.get_theme_node();
        if (this._measuredWidth === null) {
            // Copy the themed layout: measuring must never change displayed text.
            const layout = this.clutter_text.get_layout().copy();
            layout.set_width(-1);
            layout.set_ellipsize(Pango.EllipsizeMode.NONE);
            this._measuredWidth = 0;
            for (const text of this._texts) {
                layout.set_text(text, -1);
                const [width] = layout.get_pixel_size();
                this._measuredWidth = Math.max(this._measuredWidth, width);
            }
        }
        return node.adjust_preferred_width(this._measuredWidth, this._measuredWidth);
    }
});

export const WrappingLabel = GObject.registerClass(
class WrappingLabel extends St.Label {
    _init(params = {}, reservedLines = 1) {
        super._init({x_expand: true, ...params});
        this._reservedLines = reservedLines;
        this.clutter_text.set({
            line_wrap: true,
            line_wrap_mode: Pango.WrapMode.WORD_CHAR,
            ellipsize: Pango.EllipsizeMode.NONE,
        });
    }

    vfunc_get_preferred_width(_forHeight) {
        // The controls size the menu; variable readings/errors wrap within it.
        return this.get_theme_node().adjust_preferred_width(0, 0);
    }

    vfunc_get_preferred_height(forWidth) {
        const [minimum, natural] = super.vfunc_get_preferred_height(forWidth);
        const layout = this.clutter_text.get_layout().copy();
        layout.set_width(-1);
        layout.set_text(Array(this._reservedLines).fill('M').join('\n'), -1);
        const [, height] = layout.get_pixel_size();
        const [reserved] = this.get_theme_node().adjust_preferred_height(height, height);
        return [Math.max(minimum, reserved), Math.max(natural, reserved)];
    }
});
