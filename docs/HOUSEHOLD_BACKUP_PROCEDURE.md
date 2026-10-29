# Household Data Backup and Restore Procedure

This document outlines a tested procedure for backing up and restoring household data for FamClaw installations, ensuring data integrity and recoverability.

## Overview

Household data backup and restore is critical for maintaining family information, conversation history, and system configurations. This procedure covers all essential data that should be preserved during household data management operations.

> **Path conventions.** All paths use `/home/user` as a generic placeholder home
> directory and `/home/user/backups` as the default backup destination. Substitute
> your actual home directory and any backup location you prefer (a local `~/backups`,
> a mounted external drive, or a second machine) before running the commands.

## Data Categories

### 1. Conversation Data
- All conversation histories
- Message metadata and timestamps
- Attachment storage (if applicable)

### 2. Configuration Files
- Main configuration (`config.yaml`)
- Gateway configurations
- Tool and skill configurations

### 3. Family Information
- Family facts (allergies, dietary restrictions, etc.)
- User memories (personal notes, preferences)
- Family member details and roles

### 4. System Data
- Database files (SQLite)
- Credentials vault (encrypted)
- Session data and logs

## Backup Procedure

### Prerequisites
- FamClaw service should be stopped or in a consistent state
- Sufficient storage space for backup archive
- Proper permissions for accessing data directories

### Step-by-Step Process

1. **Prepare for Backup**
   ```bash
   # Stop FamClaw service if running
   sudo systemctl stop famclaw
   
   # Create backup directory
   mkdir -p /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)
   ```

2. **Backup Core Directories**
   ```bash
   # Backup database
   cp -r /home/user/famclaw-data /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/data
   
   # Backup configuration
   cp -r /home/user/.config/famclaw /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/config
   
   # Backup logs (optional but recommended)
   cp -r /home/user/famclaw-logs /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/logs
   ```

3. **Archive Backup**
   ```bash
   # Create compressed archive
   tar -czf /home/user/backups/famclaw-backup-$(date +%Y%m%d-%H%M%S).tar.gz \
     -C /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S) .
   
   # Verify archive integrity
   tar -tzf /home/user/backups/famclaw-backup-$(date +%Y%m%d-%H%M%S).tar.gz
   ```

4. **Post-Backup Verification**
   ```bash
   # Verify backup location
   ls -la /home/user/backups/
   
   # Check backup size
   du -sh /home/user/backups/famclaw-backup-$(date +%Y%m%d-%H%M%S).tar.gz
   ```

## Restore Procedure

### Prerequisites
- Fresh FamClaw installation or existing installation with clean data directory
- Backup archive file
- Proper permissions for data restoration

### Step-by-Step Process

1. **Prepare for Restore**
   ```bash
   # Stop FamClaw service if running
   sudo systemctl stop famclaw
   
   # Backup current data (if needed)
   mv /home/user/famclaw-data /home/user/famclaw-data-$(date +%Y%m%d-%H%M%S)-backup
   ```

2. **Restore Core Directories**
   ```bash
   # Extract backup
   tar -xzf /home/user/backups/famclaw-backup-$(date +%Y%m%d-%H%M%S).tar.gz \
     -C /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/
   
   # Copy restored data
   cp -r /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/data/* /home/user/famclaw-data/
   cp -r /home/user/backups/famclaw-$(date +%Y%m%d-%H%M%S)/config/* /home/user/.config/famclaw/
   ```

3. **Set Proper Permissions**
   ```bash
   # Set correct ownership
   chown -R user:user /home/user/famclaw-data
   chown -R user:user /home/user/.config/famclaw
   
   # Set correct permissions
   chmod -R 600 /home/user/famclaw-data/*
   chmod -R 600 /home/user/.config/famclaw/*
   ```

4. **Verify Restore**
   ```bash
   # Check data integrity
   ls -la /home/user/famclaw-data/
   ls -la /home/user/.config/famclaw/
   
   # Start FamClaw service
   sudo systemctl start famclaw
   
   # Monitor startup
   journalctl -u famclaw -f
   ```

## Testing Procedure

### Round-Trip Test
1. **Setup**: Install FamClaw on a test machine with sample data
2. **Backup**: Execute the backup procedure
3. **Restore**: Perform restore on a clean installation
4. **Validation**: Verify all data is intact and functional

### Verification Steps
```bash
# Check conversation data integrity
sqlite3 /home/user/famclaw-data/famclaw.db "SELECT COUNT(*) FROM conversations;"

# Verify configuration files
ls -la /home/user/.config/famclaw/

# Validate database structure
sqlite3 /home/user/famclaw-data/famclaw.db ".schema"
```

## Automation Script

### Backup Script (`/home/user/famclaw-backup.sh`)
```bash
#!/bin/bash
# Automated FamClaw backup script

BACKUP_DIR="/home/user/backups"
DATE=$(date +%Y%m%d-%H%M%S)

echo "Starting FamClaw backup..."

# Stop service
sudo systemctl stop famclaw

# Create backup directory
mkdir -p "${BACKUP_DIR}/famclaw-${DATE}"

# Copy data
cp -r /home/user/famclaw-data "${BACKUP_DIR}/famclaw-${DATE}/data"
cp -r /home/user/.config/famclaw "${BACKUP_DIR}/famclaw-${DATE}/config"

# Create archive
tar -czf "${BACKUP_DIR}/famclaw-backup-${DATE}.tar.gz" \
  -C "${BACKUP_DIR}" "famclaw-${DATE}"

# Clean up temporary directory
rm -rf "${BACKUP_DIR}/famclaw-${DATE}"

# Start service
sudo systemctl start famclaw

echo "Backup complete: ${BACKUP_DIR}/famclaw-backup-${DATE}.tar.gz"
```

### Restore Script (`/home/user/famclaw-restore.sh`)
```bash
#!/bin/bash
# Automated FamClaw restore script

BACKUP_FILE="/home/user/backups/famclaw-backup-$(date +%Y%m%d-%H%M%S).tar.gz"

echo "Starting FamClaw restore..."

# Stop service
sudo systemctl stop famclaw

# Backup current data
mv /home/user/famclaw-data "/home/user/famclaw-data-$(date +%Y%m%d-%H%M%S)-backup"

# Extract and restore
tar -xzf "${BACKUP_FILE}" -C /home/user/
cp -r /home/user/data/* /home/user/famclaw-data/
cp -r /home/user/config/* /home/user/.config/famclaw/

# Set permissions
chown -R user:user /home/user/famclaw-data
chown -R user:user /home/user/.config/famclaw

# Start service
sudo systemctl start famclaw

echo "Restore complete"
```

## Security Considerations

### Data Encryption
- Backup files should be encrypted if stored on external media
- Use GPG for encryption: `gpg --symmetric --cipher-algo AES256 backup.tar.gz`

### Access Control
- Restrict backup file permissions to owner only
- Store backups in secure locations
- Remove temporary files after backup

### Audit Trail
- Log all backup and restore operations
- Record backup timestamps and file sizes
- Maintain version history of backups

## Maintenance Schedule

### Daily Operations
- Monitor backup file integrity
- Verify backup jobs complete successfully

### Weekly Operations
- Rotate backup files (keep 7 most recent)
- Validate restore process with test data

### Monthly Operations
- Archive backups to long-term storage
- Update backup procedures based on system changes

## Troubleshooting

### Common Issues
1. **Permissions Denied**: Ensure proper ownership and permissions
2. **Database Locks**: Stop service before backup/restore
3. **Space Constraints**: Verify sufficient disk space
4. **Corrupted Archives**: Validate backup integrity before restore

### Recovery Scenarios
1. **Partial Restore**: Restore only critical data directories
2. **Version Rollback**: Use older backup if current is corrupted
3. **Multi-Device Recovery**: Apply same procedure on different hardware

## Documentation

### Version History
- v1.0 (2026-10-28): Initial release
- v1.1 (2026-10-28): Added automation scripts and testing procedures

### Contact Information
- System Administrator: [Admin Name]
- Support Email: [Support Email]
- Documentation Version: 1.1